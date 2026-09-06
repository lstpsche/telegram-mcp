package telegram

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"

	gotdtelegram "github.com/gotd/td/telegram"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"github.com/lstpsche/telegram-mcp/internal/model"
)

const mediaChunkBytes = 64 * 1024

// mediaLocation exists only while normalizing or downloading the exact source.
// Its reference, access hash, and Telegram media ID never leave this adapter.
type mediaLocation struct {
	source   model.MediaSource
	dc       int
	location tg.InputFileLocationClass
}

type photoRendition struct {
	kind          string
	width, height int
	size          int64
}

func normalizeMedia(message *tg.Message) *mediaLocation {
	if (message.Mentioned && message.MediaUnread && !voiceDocument(message)) || message.VideoProcessingPending || message.PaidSuggestedPostStars || message.PaidSuggestedPostTon || message.PaidMessageStars != 0 || !message.SuggestedPost.Zero() {
		return nil
	}
	switch media := message.Media.(type) {
	case *tg.MessageMediaPhoto:
		photo, ok := media.Photo.(*tg.Photo)
		if !ok || photo == nil || media.Spoiler || media.LivePhoto || media.Video != nil || media.TTLSeconds != 0 || media.Flags.Has(2) || photo.HasStickers || len(photo.VideoSizes) != 0 || photo.ID == 0 || photo.AccessHash == 0 || len(photo.FileReference) == 0 || len(photo.FileReference) > 4096 || photo.DCID < 1 || len(photo.Sizes) > 20 {
			return nil
		}
		var sizes []photoRendition
		seen := map[string]bool{}
		for _, value := range photo.Sizes {
			var size photoRendition
			switch value := value.(type) {
			case *tg.PhotoSize:
				size = photoRendition{value.Type, value.W, value.H, int64(value.Size)}
			case *tg.PhotoSizeProgressive:
				if len(value.Sizes) == 0 || len(value.Sizes) > 20 {
					return nil
				}
				previous := 0
				for _, length := range value.Sizes {
					if length <= previous {
						return nil
					}
					previous = length
				}
				size = photoRendition{value.Type, value.W, value.H, int64(previous)}
			default:
				continue // Embedded previews are not complete downloadable renditions.
			}
			if len(size.kind) != 1 || size.kind[0] < 'a' || size.kind[0] > 'z' || seen[size.kind] || size.width <= 0 || size.height <= 0 || size.size <= 0 {
				return nil
			}
			seen[size.kind] = true
			if size.width <= model.MaximumImageDimension && size.height <= model.MaximumImageDimension && int64(size.width)*int64(size.height) <= model.MaximumImagePixels && size.size <= model.MaximumImageBytes {
				sizes = append(sizes, size)
			}
		}
		if len(sizes) == 0 {
			return nil
		}
		sort.Slice(sizes, func(i, j int) bool {
			a, b := sizes[i], sizes[j]
			if a.width*a.height != b.width*b.height {
				return a.width*a.height > b.width*b.height
			}
			return a.kind < b.kind
		})
		size := sizes[0]
		source := mediaIdentity("photo", "image/jpeg", photo.ID, size)
		return &mediaLocation{source: source, dc: photo.DCID, location: &tg.InputPhotoFileLocation{ID: photo.ID, AccessHash: photo.AccessHash, FileReference: photo.FileReference, ThumbSize: size.kind}}
	case *tg.MessageMediaDocument:
		document, ok := media.Document.(*tg.Document)
		if !ok || document == nil || media.Spoiler || media.Video || media.Round || media.Nopremium || media.TTLSeconds != 0 || media.Flags.Has(2) || len(media.AltDocuments) != 0 || media.VideoCover != nil || media.VideoTimestamp != 0 || document.ID == 0 || document.AccessHash == 0 || len(document.FileReference) == 0 || len(document.FileReference) > 4096 || document.DCID < 1 || len(document.VideoThumbs) != 0 || (document.MimeType != "image/jpeg" && document.MimeType != "image/png" && document.MimeType != "application/pdf" && document.MimeType != "text/plain" && document.MimeType != "audio/ogg") || len(document.Attributes) > 2 {
			return nil
		}
		var audio *tg.DocumentAttributeAudio
		var dimensions *tg.DocumentAttributeImageSize
		filename := false
		for _, value := range document.Attributes {
			switch value := value.(type) {
			case *tg.DocumentAttributeAudio:
				if audio != nil || !value.Voice || value.Duration <= 0 || value.Duration > model.MaximumVoiceDuration || value.Title != "" || value.Performer != "" || len(value.Waveform) > 1024 {
					return nil
				}
				audio = value
			case *tg.DocumentAttributeImageSize:
				if dimensions != nil {
					return nil
				}
				dimensions = value
			case *tg.DocumentAttributeFilename:
				if filename {
					return nil
				}
				filename = true
			default:
				return nil
			}
		}
		isDocument := document.MimeType == "application/pdf" || document.MimeType == "text/plain"
		rendition := photoRendition{size: document.Size}
		isVoice := document.MimeType == "audio/ogg"
		if media.Voice && !isVoice {
			return nil
		}
		if isVoice {
			if audio == nil || dimensions != nil {
				return nil
			}
		} else if audio != nil {
			return nil
		}
		if isDocument || isVoice {
			if dimensions != nil {
				return nil
			}
		} else {
			if dimensions == nil {
				return nil
			}
			rendition.width, rendition.height = dimensions.W, dimensions.H
		}
		kind := "document"
		if isVoice {
			kind = "voice"
			rendition.kind = fmt.Sprintf("opus:%d", audio.Duration)
		}
		source := mediaIdentity(kind, document.MimeType, document.ID, rendition)
		if isVoice {
			source.Duration = audio.Duration
		}
		if source.Validate() != nil {
			return nil
		}
		return &mediaLocation{source: source, dc: document.DCID, location: &tg.InputDocumentFileLocation{ID: document.ID, AccessHash: document.AccessHash, FileReference: document.FileReference}}
	default:
		return nil
	}
}

func mediaIdentity(kind, mime string, id int64, rendition photoRendition) model.MediaSource {
	// This canonical digest excludes mutable authorization and routing metadata.
	identity := struct {
		Kind, MIMEType string
		ID             int64
		Rendition      string
		Width, Height  int
		Size           int64
	}{kind, mime, id, rendition.kind, rendition.width, rendition.height, rendition.size}
	encoded, _ := json.Marshal(identity) // Fixed primitive fields cannot fail to encode.
	digest := sha256.Sum256(encoded)
	return model.MediaSource{Kind: kind, MIMEType: mime, Width: rendition.width, Height: rendition.height, Size: rendition.size, Fingerprint: hex.EncodeToString(digest[:])}
}

func candidateMedia(candidate model.Candidate) *model.MediaSource {
	count := 0
	var result *model.MediaSource
	for _, source := range []*model.MediaSource{candidate.Image, candidate.Document, candidate.Voice} {
		if source != nil {
			count++
			result = source
		}
	}
	if count != 1 {
		return nil
	}
	if candidate.Voice != nil && !result.IsVoice() || candidate.Document != nil && !result.IsDocument() || candidate.Image != nil && (result.IsDocument() || result.IsVoice()) {
		return nil
	}
	return result
}

func voiceDocument(message *tg.Message) bool {
	media, ok := message.Media.(*tg.MessageMediaDocument)
	if !ok {
		return false
	}
	d, ok := media.Document.(*tg.Document)
	if !ok {
		return false
	}
	for _, attribute := range d.Attributes {
		if a, ok := attribute.(*tg.DocumentAttributeAudio); ok && a.Voice {
			return true
		}
	}
	return false
}

func (a *Account) DownloadVoice(ctx context.Context, expected model.Candidate) ([]byte, error) {
	if expected.Voice == nil || expected.Image != nil || expected.Document != nil || !expected.Voice.IsVoice() {
		return nil, model.TextError(model.ErrorInvalidReference, nil)
	}
	return a.downloadMedia(ctx, expected)
}

func safeMedia(candidate model.Candidate) bool {
	return candidateMedia(candidate) != nil && candidateMedia(candidate).Validate() == nil && !candidate.Protected && !candidate.Ephemeral && !candidate.Forwarded && !candidate.Quoted && !candidate.Unsupported
}

func (a *Account) exactMedia(ctx context.Context, expected model.Candidate) (*mediaLocation, error) {
	if !a.Ready() {
		return nil, model.TextError(model.ErrorFreshnessDegraded, nil)
	}
	if !safeMedia(expected) {
		return nil, model.TextError(model.ErrorInvalidReference, nil)
	}
	peer, id := expected.Message.ID.Peer(), expected.Message.ID.TelegramID()
	if id <= 0 {
		return nil, model.TextError(model.ErrorInvalidReference, nil)
	}
	chat, err := a.Chat(ctx, peer)
	if err != nil {
		return nil, err
	}
	if chat.Forum {
		return nil, model.TextError(model.ErrorUnsupportedPeer, nil)
	}
	input, err := a.reads.inputPeer(ctx, peer)
	if err != nil {
		return nil, err
	}
	maximum := 0
	if id < math.MaxInt32 {
		maximum = int(id) + 1
	}
	result, err := a.reads.historyPage(ctx, peer, &tg.MessagesGetHistoryRequest{Peer: input, OffsetID: int(id), AddOffset: -1, Limit: 1, MinID: int(id) - 1, MaxID: maximum})
	if err != nil {
		return nil, readError(err)
	}
	candidates, err := a.reads.normalizePage(ctx, peer, result, 1)
	if err != nil {
		return nil, err
	}
	if len(candidates) != 1 || !safeMedia(candidates[0]) || candidates[0].Message.ID != expected.Message.ID || candidates[0].Message.Author != expected.Message.Author || *candidateMedia(candidates[0]) != *candidateMedia(expected) {
		return nil, model.TextError(model.ErrorInvalidReference, nil)
	}
	// normalizePage validated the constructor and exact candidate above.
	page := result.(messagePage)
	message, ok := page.GetMessages()[0].(*tg.Message)
	if !ok {
		return nil, model.TextError(model.ErrorInvalidReference, nil)
	}
	location := normalizeMedia(message)
	if location == nil || location.source != *candidateMedia(expected) {
		return nil, model.TextError(model.ErrorInvalidReference, nil)
	}
	if err := a.reads.synchronize(ctx); err != nil {
		return nil, model.TextError(model.ErrorFreshnessDegraded, err)
	}
	return location, nil
}

// The SDK resolves DC IDs against the selected environment's current DC options.
// Media metadata supplies an ID, never an endpoint or a Test-DC-only range.
func (a *Account) mediaAPI(ctx context.Context, dc int) (*tg.Client, gotdtelegram.CloseInvoker, error) {
	if dc < 1 || a.openImageDC == nil {
		return nil, nil, model.TextError(model.ErrorInvalidReference, nil)
	}
	pool, err := a.openImageDC(ctx, dc)
	if err != nil {
		return nil, nil, readError(err)
	}
	if pool == nil {
		return nil, nil, model.TextError(model.ErrorTelegramUnavailable, errors.New("Telegram media pool is missing"))
	}
	var invoker tg.Invoker = pool
	// Explicit pools avoid the primary API's automatic FILE_MIGRATE routing.
	// They do not apply the primary client's middleware automatically.
	for i := len(a.middlewares) - 1; i >= 0; i-- {
		invoker = a.middlewares[i].Handle(invoker)
	}
	return tg.NewClient(invoker), pool, nil
}

// DownloadImage fetches only the unchanged source approved by the caller.
// The caller remains responsible for policy and read acknowledgment before use.
func (a *Account) DownloadImage(ctx context.Context, expected model.Candidate) ([]byte, error) {
	if expected.Image == nil || expected.Document != nil || expected.Voice != nil || expected.Image.IsDocument() || expected.Image.IsVoice() {
		return nil, model.TextError(model.ErrorInvalidReference, nil)
	}
	return a.downloadMedia(ctx, expected)
}

func (a *Account) DownloadDocument(ctx context.Context, expected model.Candidate) ([]byte, error) {
	if expected.Document == nil || expected.Image != nil || expected.Voice != nil || !expected.Document.IsDocument() {
		return nil, model.TextError(model.ErrorInvalidReference, nil)
	}
	return a.downloadMedia(ctx, expected)
}

func (a *Account) downloadMedia(ctx context.Context, expected model.Candidate) (data []byte, resultErr error) {
	bounded, cancel := context.WithTimeout(ctx, readDeadline)
	defer cancel()
	location, err := a.exactMedia(bounded, expected)
	if err != nil {
		return nil, err
	}
	api, pool, err := a.mediaAPI(bounded, location.dc)
	if err != nil {
		return nil, err
	}
	buffer := make([]byte, 0, int(min(location.source.Size, int64(mediaChunkBytes))))
	defer func() {
		if pool != nil {
			if err := pool.Close(); err != nil {
				resultErr = errors.Join(resultErr, readError(err))
			}
		}
		if resultErr == nil {
			if err := bounded.Err(); err != nil {
				resultErr = readError(err)
			} else if !a.Ready() {
				resultErr = model.TextError(model.ErrorFreshnessDegraded, nil)
			}
		}
		if resultErr != nil {
			clear(buffer)
			data = nil
		}
	}()
	renewed := false
	// Exact-size chunks bound work; allow one additional reference renewal.
	maximumCalls := (location.source.Size-1)/mediaChunkBytes + 2
	for calls := int64(0); int64(len(buffer)) < location.source.Size; calls++ {
		if calls >= maximumCalls {
			return nil, model.TextError(model.ErrorResultTooLarge, nil)
		}
		if err := bounded.Err(); err != nil {
			return nil, readError(err)
		}
		if !a.Ready() {
			return nil, model.TextError(model.ErrorFreshnessDegraded, nil)
		}
		result, err := api.UploadGetFile(bounded, &tg.UploadGetFileRequest{Location: location.location, Offset: int64(len(buffer)), Limit: mediaChunkBytes})
		if err != nil {
			if !renewed && tgerr.Is(err, "FILE_REFERENCE_EXPIRED", "FILE_REFERENCE_INVALID") {
				renewed = true
				refreshed, refreshErr := a.exactMedia(bounded, expected)
				if refreshErr != nil {
					return nil, refreshErr
				}
				if refreshed.dc != location.dc {
					if pool != nil {
						closeErr := pool.Close()
						pool = nil
						if closeErr != nil {
							return nil, readError(closeErr)
						}
					}
					api, pool, refreshErr = a.mediaAPI(bounded, refreshed.dc)
					if refreshErr != nil {
						return nil, refreshErr
					}
				}
				location = refreshed
				continue
			}
			return nil, readError(err)
		}
		file, ok := result.(*tg.UploadFile)
		if !ok {
			return nil, model.TextError(model.ErrorTelegramUnavailable, errors.New("Telegram media response is unsupported"))
		}
		validType := false
		switch file.Type.(type) {
		case *tg.StorageFileUnknown, *tg.StorageFilePartial:
			// These constructors make no encoding claim. The caller validates
			// the complete bytes against the authorized media descriptor.
			validType = true
		case *tg.StorageFileJpeg:
			validType = location.source.MIMEType == "image/jpeg"
		case *tg.StorageFilePdf:
			validType = location.source.MIMEType == "application/pdf"
		case *tg.StorageFilePng:
			validType = location.source.MIMEType == "image/png"
		}
		expectedBytes := int(min(int64(mediaChunkBytes), location.source.Size-int64(len(buffer))))
		if !validType || len(file.Bytes) != expectedBytes {
			return nil, model.TextError(model.ErrorInvalidReference, errors.New("Telegram media chunk does not match its descriptor"))
		}
		buffer = append(buffer, file.Bytes...)
	}
	if err := bounded.Err(); err != nil {
		return nil, readError(err)
	}
	if !a.Ready() {
		return nil, model.TextError(model.ErrorFreshnessDegraded, nil)
	}
	return buffer, nil
}

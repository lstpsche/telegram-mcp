package telegram

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gotd/td/bin"
	gotdtelegram "github.com/gotd/td/telegram"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"github.com/lstpsche/telegram-mcp/internal/model"
)

func testPhotoMessage() *tg.Message {
	message := testMessage(10)
	message.Media = &tg.MessageMediaPhoto{Photo: &tg.Photo{ID: 123, AccessHash: 456, FileReference: []byte("private-file-reference"), DCID: 2, Date: 99, Sizes: []tg.PhotoSizeClass{&tg.PhotoSize{Type: "m", W: 32, H: 32, Size: 100}}}}
	return message
}

func testDocumentMessage() *tg.Message {
	message := testMessage(10)
	message.Media = &tg.MessageMediaDocument{Document: &tg.Document{ID: 123, AccessHash: 456, FileReference: []byte("private-document-reference"), DCID: 2, Date: 99, MimeType: "image/png", Size: 100, Attributes: []tg.DocumentAttributeClass{&tg.DocumentAttributeImageSize{W: 32, H: 32}, &tg.DocumentAttributeFilename{FileName: "private-name.png"}}}}
	return message
}

func imageCandidate(t *testing.T, message *tg.Message) model.Candidate {
	t.Helper()
	candidate, err := normalizeMessage(testSelfPeer(t), 1, message, map[int64]bool{1: true})
	if err != nil || !safeMedia(candidate) {
		t.Fatalf("image normalization failed: %v", err)
	}
	return candidate
}

func TestImageNormalizationSupportsCaptionlessPhotosAndStaticDocuments(t *testing.T) {
	for _, message := range []*tg.Message{testPhotoMessage(), testDocumentMessage()} {
		message.Message = ""
		candidate := imageCandidate(t, message)
		if candidate.Message.Text != "" || candidate.Message.Date == "" || candidate.Image.Width != 32 {
			t.Fatal("captionless image metadata missing")
		}
	}
	message := testPhotoMessage()
	photo := message.Media.(*tg.MessageMediaPhoto).Photo.(*tg.Photo)
	photo.Sizes = []tg.PhotoSizeClass{
		&tg.PhotoStrippedSize{Type: "i", Bytes: []byte("not a complete image")},
		&tg.PhotoSize{Type: "x", W: 4096, H: 4096, Size: 100},
		&tg.PhotoSizeProgressive{Type: "y", W: 100, H: 100, Sizes: []int{100, 200}},
		&tg.PhotoSize{Type: "m", W: 100, H: 100, Size: 150},
	}
	first := imageCandidate(t, message)
	if first.Image.Size != 150 || first.Image.Width != 100 {
		t.Fatal("bounded deterministic rendition selection failed")
	}
	photo.AccessHash++
	photo.FileReference = []byte("renewed-private-reference")
	photo.DCID = 3
	second := imageCandidate(t, message)
	if *first.Image != *second.Image {
		t.Fatal("authorization or routing metadata changed media identity")
	}
	photo.ID++
	if imageCandidate(t, message).Image.Fingerprint == first.Image.Fingerprint {
		t.Fatal("replacement media retained its identity")
	}
}

func TestUnsafeMediasNeverExposeCaptionsOrDescriptors(t *testing.T) {
	for name, mutate := range map[string]func(*tg.Message){
		"protected":        func(m *tg.Message) { m.Noforwards = true },
		"message ttl":      func(m *tg.Message) { m.TTLPeriod = 5 },
		"media ttl":        func(m *tg.Message) { m.Media.(*tg.MessageMediaPhoto).TTLSeconds = 5 },
		"present zero ttl": func(m *tg.Message) { m.Media.(*tg.MessageMediaPhoto).Flags.Set(2) },
		"spoiler":          func(m *tg.Message) { m.Media.(*tg.MessageMediaPhoto).Spoiler = true },
		"live photo":       func(m *tg.Message) { m.Media.(*tg.MessageMediaPhoto).LivePhoto = true },
		"photo video":      func(m *tg.Message) { m.Media.(*tg.MessageMediaPhoto).Video = &tg.DocumentEmpty{} },
		"stickers":         func(m *tg.Message) { m.Media.(*tg.MessageMediaPhoto).Photo.(*tg.Photo).HasStickers = true },
		"unread mention":   func(m *tg.Message) { m.Mentioned, m.MediaUnread = true, true },
		"paid message":     func(m *tg.Message) { m.PaidMessageStars = 1 },
		"paid suggestion":  func(m *tg.Message) { m.PaidSuggestedPostStars = true },
		"processing video": func(m *tg.Message) { m.VideoProcessingPending = true },
		"malformed progressive": func(m *tg.Message) {
			m.Media.(*tg.MessageMediaPhoto).Photo.(*tg.Photo).Sizes = []tg.PhotoSizeClass{&tg.PhotoSizeProgressive{Type: "x", W: 10, H: 10, Sizes: []int{100, 90}}}
		},
		"ambiguous rendition": func(m *tg.Message) {
			p := m.Media.(*tg.MessageMediaPhoto).Photo.(*tg.Photo)
			p.Sizes = append(p.Sizes, p.Sizes[0])
		},
		"oversized image": func(m *tg.Message) {
			m.Media.(*tg.MessageMediaPhoto).Photo.(*tg.Photo).Sizes[0].(*tg.PhotoSize).Size = model.MaximumImageBytes + 1
		},
		"invalid dc": func(m *tg.Message) { m.Media.(*tg.MessageMediaPhoto).Photo.(*tg.Photo).DCID = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			message := testPhotoMessage()
			mutate(message)
			candidate, err := normalizeMessage(testSelfPeer(t), 1, message, map[int64]bool{1: true})
			if err != nil || candidate.Image != nil || candidate.Message.Text != "" || candidate.Message.Date != "" {
				t.Fatalf("unsafe image escaped: %v", err)
			}
		})
	}
	for name, mutate := range map[string]func(*tg.Document, *tg.MessageMediaDocument){
		"animation": func(d *tg.Document, _ *tg.MessageMediaDocument) {
			d.Attributes = []tg.DocumentAttributeClass{&tg.DocumentAttributeAnimated{}}
		},
		"sticker": func(d *tg.Document, _ *tg.MessageMediaDocument) {
			d.Attributes = []tg.DocumentAttributeClass{&tg.DocumentAttributeSticker{}}
		},
		"video": func(_ *tg.Document, m *tg.MessageMediaDocument) { m.Video = true },
		"voice": func(_ *tg.Document, m *tg.MessageMediaDocument) { m.Voice = true },
		"cover": func(_ *tg.Document, m *tg.MessageMediaDocument) { m.VideoCover = &tg.PhotoEmpty{} },
		"alternative": func(_ *tg.Document, m *tg.MessageMediaDocument) {
			m.AltDocuments = []tg.DocumentClass{&tg.DocumentEmpty{}}
		},
		"spoiler":              func(_ *tg.Document, m *tg.MessageMediaDocument) { m.Spoiler = true },
		"ttl":                  func(_ *tg.Document, m *tg.MessageMediaDocument) { m.TTLSeconds = 1 },
		"mime":                 func(d *tg.Document, _ *tg.MessageMediaDocument) { d.MimeType = "image/gif" },
		"duplicate dimensions": func(d *tg.Document, _ *tg.MessageMediaDocument) { d.Attributes[1] = d.Attributes[0] },
		"oversized":            func(d *tg.Document, _ *tg.MessageMediaDocument) { d.Size = model.MaximumImageBytes + 1 },
	} {
		t.Run("document "+name, func(t *testing.T) {
			message := testDocumentMessage()
			media := message.Media.(*tg.MessageMediaDocument)
			mutate(media.Document.(*tg.Document), media)
			candidate, err := normalizeMessage(testSelfPeer(t), 1, message, map[int64]bool{1: true})
			if err != nil || candidate.Image != nil || candidate.Message.Text != "" {
				t.Fatalf("unsafe attachment escaped: %v", err)
			}
		})
	}
}

func imageTestAccount(t *testing.T, source func() *tg.Message, download func(*tg.UploadGetFileRequest) (tg.UploadFileClass, error)) *Account {
	t.Helper()
	invoke := gotdtelegram.InvokeFunc(func(ctx context.Context, input bin.Encoder, out bin.Decoder) error {
		switch q := input.(type) {
		case *tg.UpdatesGetStateRequest:
			return encodeReadResponse(out, &tg.UpdatesState{Pts: 10, Date: 100, Seq: 1})
		case *tg.MessagesGetHistoryRequest:
			if _, ok := q.Peer.(*tg.InputPeerSelf); !ok || q.OffsetID != 10 || q.MinID != 9 || q.MaxID != 11 || q.AddOffset != -1 || q.Limit != 1 {
				t.Fatal("image source was not exact and peer scoped")
			}
			return encodeReadResponse(out, &tg.MessagesMessages{Messages: []tg.MessageClass{source()}})
		case *tg.UploadGetFileRequest:
			result, err := download(q)
			if err != nil {
				return err
			}
			return encodeReadResponse(out, result)
		default:
			t.Fatalf("unexpected image RPC %T", input)
			return nil
		}
	})
	account, _ := newReadTestAccount(t, invoke)
	account.middlewares = []gotdtelegram.Middleware{readMiddleware{account.reads}}
	account.openImageDC = func(_ context.Context, dc int) (gotdtelegram.CloseInvoker, error) {
		if dc != 2 {
			t.Fatal("unexpected remote DC")
		}
		return &imageTestPool{InvokeFunc: invoke}, nil
	}
	return account
}

func TestImageDownloadRenewsExactReferenceOnceAndResumesBoundedOffset(t *testing.T) {
	message := testPhotoMessage()
	photo := message.Media.(*tg.MessageMediaPhoto).Photo.(*tg.Photo)
	photo.Sizes[0].(*tg.PhotoSize).Size = mediaChunkBytes + 5
	expected := imageCandidate(t, message)
	calls, sources := 0, 0
	account := imageTestAccount(t, func() *tg.Message {
		sources++
		if sources == 2 {
			photo.FileReference = []byte("renewed")
		}
		return message
	}, func(q *tg.UploadGetFileRequest) (tg.UploadFileClass, error) {
		calls++
		if q.CDNSupported || q.Precise || q.Limit != mediaChunkBytes {
			t.Fatal("unbounded image request")
		}
		if calls == 1 {
			if q.Offset != 0 {
				t.Fatal("initial offset")
			}
			return &tg.UploadFile{Type: &tg.StorageFileJpeg{}, Bytes: bytes.Repeat([]byte{7}, mediaChunkBytes)}, nil
		}
		if q.Offset != mediaChunkBytes {
			t.Fatal("renewal restarted already downloaded bytes")
		}
		if calls == 2 {
			return nil, tgerr.New(400, "FILE_REFERENCE_EXPIRED")
		}
		if !bytes.Equal(q.Location.(*tg.InputPhotoFileLocation).FileReference, []byte("renewed")) {
			t.Fatal("old reference reused")
		}
		return &tg.UploadFile{Type: &tg.StorageFileJpeg{}, Bytes: bytes.Repeat([]byte{8}, 5)}, nil
	})
	data, err := account.DownloadImage(context.Background(), expected)
	if err != nil || len(data) != mediaChunkBytes+5 || calls != 3 || sources != 2 || data[0] != 7 || data[len(data)-1] != 8 {
		t.Fatalf("renewal result: bytes=%d calls=%d sources=%d err=%v", len(data), calls, sources, err)
	}
}

func TestImageDownloadRejectsChangedSourceBeforeBytes(t *testing.T) {
	for name, mutate := range map[string]func(*tg.Message){
		"media":      func(m *tg.Message) { m.Media.(*tg.MessageMediaPhoto).Photo.(*tg.Photo).ID++ },
		"author":     func(m *tg.Message) { m.FromID = &tg.PeerUser{UserID: 99} },
		"message":    func(m *tg.Message) { m.ID++ },
		"protection": func(m *tg.Message) { m.Noforwards = true },
	} {
		t.Run(name, func(t *testing.T) {
			message := testPhotoMessage()
			expected := imageCandidate(t, message)
			mutate(message)
			account := imageTestAccount(t, func() *tg.Message { return message }, func(*tg.UploadGetFileRequest) (tg.UploadFileClass, error) {
				t.Fatal("downloaded replacement source")
				return nil, nil
			})
			if data, err := account.DownloadImage(context.Background(), expected); err == nil || data != nil {
				t.Fatal("replacement image accepted")
			}
		})
	}
}

func TestImageDownloadRejectsReplacementDuringReferenceRenewal(t *testing.T) {
	message := testPhotoMessage()
	expected := imageCandidate(t, message)
	sources, downloads := 0, 0
	account := imageTestAccount(t, func() *tg.Message {
		sources++
		if sources == 2 {
			message.Media.(*tg.MessageMediaPhoto).Photo.(*tg.Photo).ID++
		}
		return message
	}, func(*tg.UploadGetFileRequest) (tg.UploadFileClass, error) {
		downloads++
		return nil, tgerr.New(400, "FILE_REFERENCE_EXPIRED")
	})
	data, err := account.DownloadImage(context.Background(), expected)
	if err == nil || data != nil || sources != 2 || downloads != 1 {
		t.Fatal("reference renewal downloaded replacement media")
	}
}

func TestImageDownloadMaximumSizeHasFiniteChunkBudget(t *testing.T) {
	message := testPhotoMessage()
	message.Media.(*tg.MessageMediaPhoto).Photo.(*tg.Photo).Sizes[0].(*tg.PhotoSize).Size = model.MaximumImageBytes
	calls := 0
	account := imageTestAccount(t, func() *tg.Message { return message }, func(q *tg.UploadGetFileRequest) (tg.UploadFileClass, error) {
		calls++
		if calls == 1 {
			return nil, tgerr.New(400, "FILE_REFERENCE_EXPIRED")
		}
		if q.Offset != int64(calls-2)*mediaChunkBytes || q.Limit != mediaChunkBytes {
			t.Fatal("unaligned image download")
		}
		return &tg.UploadFile{Type: &tg.StorageFileJpeg{}, Bytes: make([]byte, mediaChunkBytes)}, nil
	})
	data, err := account.DownloadImage(context.Background(), imageCandidate(t, message))
	if err != nil || len(data) != model.MaximumImageBytes || calls != 17 {
		t.Fatalf("maximum image bounds: calls=%d bytes=%d err=%v", calls, len(data), err)
	}
}

func TestImageDownloadCancellationDoesNotReleaseBytes(t *testing.T) {
	message := testPhotoMessage()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	account := imageTestAccount(t, func() *tg.Message { return message }, func(*tg.UploadGetFileRequest) (tg.UploadFileClass, error) {
		cancel()
		return &tg.UploadFile{Type: &tg.StorageFileJpeg{}, Bytes: make([]byte, 100)}, nil
	})
	data, err := account.DownloadImage(ctx, imageCandidate(t, message))
	if !errors.Is(err, context.Canceled) || data != nil {
		t.Fatal("cancelled download released bytes")
	}
}

func TestImageDownloadAllowsUnspecifiedTransportEncoding(t *testing.T) {
	for _, kind := range []tg.StorageFileTypeClass{&tg.StorageFileUnknown{}, &tg.StorageFilePartial{}} {
		message := testDocumentMessage()
		account := imageTestAccount(t, func() *tg.Message { return message }, func(*tg.UploadGetFileRequest) (tg.UploadFileClass, error) {
			return &tg.UploadFile{Type: kind, Bytes: make([]byte, 100)}, nil
		})
		data, err := account.DownloadImage(context.Background(), imageCandidate(t, message))
		if err != nil || len(data) != 100 {
			t.Fatalf("unspecified storage encoding was rejected before byte validation: %v", err)
		}
	}
}

func TestImageDownloadRejectsChunkFailuresWithoutPartialData(t *testing.T) {
	for name, response := range map[string]func() (tg.UploadFileClass, error){
		"short": func() (tg.UploadFileClass, error) {
			return &tg.UploadFile{Type: &tg.StorageFileJpeg{}, Bytes: make([]byte, 99)}, nil
		},
		"long": func() (tg.UploadFileClass, error) {
			return &tg.UploadFile{Type: &tg.StorageFileJpeg{}, Bytes: make([]byte, 101)}, nil
		},
		"mime": func() (tg.UploadFileClass, error) {
			return &tg.UploadFile{Type: &tg.StorageFilePng{}, Bytes: make([]byte, 100)}, nil
		},
		"cdn":             func() (tg.UploadFileClass, error) { return &tg.UploadFileCDNRedirect{}, nil },
		"reference twice": func() (tg.UploadFileClass, error) { return nil, tgerr.New(400, "FILE_REFERENCE_INVALID") },
		"migration":       func() (tg.UploadFileClass, error) { return nil, tgerr.New(303, "FILE_MIGRATE_3") },
	} {
		t.Run(name, func(t *testing.T) {
			message := testPhotoMessage()
			calls := 0
			account := imageTestAccount(t, func() *tg.Message { return message }, func(*tg.UploadGetFileRequest) (tg.UploadFileClass, error) { calls++; return response() })
			data, err := account.DownloadImage(context.Background(), imageCandidate(t, message))
			if err == nil || data != nil || calls > 2 || strings.Contains(err.Error(), "private") {
				t.Fatalf("invalid file released or unsafe error: %v", err)
			}
		})
	}
}

type imageTestPool struct {
	gotdtelegram.InvokeFunc
	closed   int
	closeErr error
}

func (p *imageTestPool) Close() error { p.closed++; return p.closeErr }

func TestImageReferenceRenewalMovesOnlyToCurrentSourceDC(t *testing.T) {
	message := testPhotoMessage()
	photo := message.Media.(*tg.MessageMediaPhoto).Photo.(*tg.Photo)
	expected := imageCandidate(t, message)
	sources := 0
	account := imageTestAccount(t, func() *tg.Message {
		sources++
		if sources == 2 {
			photo.DCID = 3
			photo.FileReference = []byte("renewed")
		}
		return message
	}, func(*tg.UploadGetFileRequest) (tg.UploadFileClass, error) {
		t.Fatal("unexpected shared fake download")
		return nil, nil
	})
	oldPool := &imageTestPool{InvokeFunc: func(context.Context, bin.Encoder, bin.Decoder) error { return tgerr.New(400, "FILE_REFERENCE_EXPIRED") }}
	newPool := &imageTestPool{InvokeFunc: func(ctx context.Context, input bin.Encoder, out bin.Decoder) error {
		q := input.(*tg.UploadGetFileRequest)
		if q.Offset != 0 || !bytes.Equal(q.Location.(*tg.InputPhotoFileLocation).FileReference, []byte("renewed")) {
			t.Fatal("renewal did not use the exact current source")
		}
		return encodeReadResponse(out, &tg.UploadFile{Type: &tg.StorageFileJpeg{}, Bytes: make([]byte, 100)})
	}}
	opens := 0
	account.openImageDC = func(ctx context.Context, dc int) (gotdtelegram.CloseInvoker, error) {
		opens++
		if opens == 1 && dc == 2 {
			return oldPool, nil
		}
		if opens == 2 && dc == 3 && oldPool.closed == 1 {
			return newPool, nil
		}
		t.Fatal("renewal opened an unrelated DC or retained the old pool")
		return nil, nil
	}
	data, err := account.DownloadImage(context.Background(), expected)
	if err != nil || len(data) != 100 || opens != 2 || oldPool.closed != 1 || newPool.closed != 1 {
		t.Fatalf("DC renewal failed: %v", err)
	}
}

func TestImageRemoteDCUsesSharedMiddlewareAndClosesPool(t *testing.T) {
	for _, closeFails := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "close failure"}[closeFails], func(t *testing.T) {
			message := testDocumentMessage()
			message.Media.(*tg.MessageMediaDocument).Document.(*tg.Document).DCID = 5
			account := imageTestAccount(t, func() *tg.Message { return message }, func(*tg.UploadGetFileRequest) (tg.UploadFileClass, error) { t.Fatal("wrong DC"); return nil, nil })
			calls, middlewareCalls := 0, 0
			pool := &imageTestPool{InvokeFunc: func(ctx context.Context, input bin.Encoder, out bin.Decoder) error {
				q, ok := input.(*tg.UploadGetFileRequest)
				if !ok || q.Offset != 0 || q.Location.(*tg.InputDocumentFileLocation).ThumbSize != "" {
					t.Fatal("wrong document download")
				}
				calls++
				return encodeReadResponse(out, &tg.UploadFile{Type: &tg.StorageFilePng{}, Bytes: make([]byte, 100)})
			}}
			cause := errors.New("synthetic close failure")
			if closeFails {
				pool.closeErr = cause
			}
			account.openImageDC = func(ctx context.Context, dc int) (gotdtelegram.CloseInvoker, error) {
				if dc != 5 {
					t.Fatal("wrong pool DC")
				}
				return pool, nil
			}
			account.middlewares = []gotdtelegram.Middleware{gotdtelegram.MiddlewareFunc(func(next tg.Invoker) gotdtelegram.InvokeFunc {
				return func(ctx context.Context, input bin.Encoder, out bin.Decoder) error {
					middlewareCalls++
					return next.Invoke(ctx, input, out)
				}
			}), readMiddleware{account.reads}}
			data, err := account.DownloadImage(context.Background(), imageCandidate(t, message))
			if calls != 1 || middlewareCalls != 1 || pool.closed != 1 {
				t.Fatal("remote DC lifecycle or middleware bypassed")
			}
			if closeFails {
				if !errors.Is(err, cause) || data != nil {
					t.Fatal("close failure released image or lost cause")
				}
			} else if err != nil || len(data) != 100 {
				t.Fatalf("remote image failed: %v", err)
			}
		})
	}
}

func TestCaptionlessImagesSupportProductionDataCenters(t *testing.T) {
	for _, dc := range []int{4, 5} {
		for _, message := range []*tg.Message{testPhotoMessage(), testDocumentMessage()} {
			message.Message = ""
			switch media := message.Media.(type) {
			case *tg.MessageMediaPhoto:
				media.Photo.(*tg.Photo).DCID = dc
			case *tg.MessageMediaDocument:
				media.Document.(*tg.Document).DCID = dc
			}
			candidate := imageCandidate(t, message)
			if candidate.Message.Text != "" || candidate.Image == nil {
				t.Fatal("production captionless image rejected")
			}
		}
	}
}

func TestImageDataCenterResolutionFailureReleasesNoPool(t *testing.T) {
	account, _ := newReadTestAccount(t, func(context.Context, bin.Encoder, bin.Decoder) error { t.Fatal("unexpected RPC"); return nil })
	api, pool, err := account.mediaAPI(context.Background(), 99)
	if err == nil || api != nil || pool != nil {
		t.Fatal("unknown data center was accepted")
	}
}

package telegram

import (
	"github.com/gotd/td/tg"
	"github.com/lstpsche/telegram-mcp/internal/model"
)

func normalizeLinkPreview(media *tg.MessageMediaWebPage) (*model.LinkPreview, error) {
	invalid := func() (*model.LinkPreview, error) { return nil, model.TextError(model.ErrorInvalidReference, nil) }
	if media == nil {
		return invalid()
	}
	preview := &model.LinkPreview{Manual: media.Manual}
	switch page := media.Webpage.(type) {
	case *tg.WebPage:
		if page == nil || page.ID == 0 {
			return invalid()
		}
		preview.State = "available"
		preview.URL = page.URL
		preview.SiteName, _ = page.GetSiteName()
		preview.Title, _ = page.GetTitle()
		preview.Description, _ = page.GetDescription()
	case *tg.WebPagePending:
		if page == nil || page.ID == 0 || page.Date <= 0 {
			return invalid()
		}
		preview.State = "pending"
		preview.URL, _ = page.GetURL()
	case *tg.WebPageEmpty:
		if page == nil {
			return invalid()
		}
		preview.State = "unavailable"
		preview.URL, _ = page.GetURL()
	default:
		// Not-modified requires a cache, which this content-free runtime does not own.
		return invalid()
	}
	if !preview.Valid() {
		return invalid()
	}
	return preview, nil
}

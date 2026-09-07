package model

import "time"

func (m Message) ValidateEditDate() error {
	if m.EditedAt == "" {
		return nil
	}
	sent, err := time.Parse(time.RFC3339, m.Date)
	if err != nil {
		return TextError(ErrorInvalidReference, nil)
	}
	edited, err := time.Parse(time.RFC3339, m.EditedAt)
	if err != nil || edited.Before(sent) || edited.Unix() <= 0 {
		return TextError(ErrorInvalidReference, nil)
	}
	return nil
}

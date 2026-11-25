package fakes

import (
	"context"

	"github.com/TheTangentLine/deployctl/internal/steps"
)

var _ steps.History = (*History)(nil)

// History is a fake deployment history.
type History struct {
	// Tags maps "app/environment" to successfully deployed tags, newest first.
	Tags map[string][]string
	Err  error
}

func (h *History) LastGoodTag(ctx context.Context, app, environment, exclude string) (string, bool, error) {
	if h.Err != nil {
		return "", false, h.Err
	}
	for _, t := range h.Tags[app+"/"+environment] {
		if t != exclude {
			return t, true, nil
		}
	}
	return "", false, nil
}

package tools

import (
	"errors"

	"github.com/h2cone/serpe/internal/imagecheck"
)

const (
	mimePNG  = imagecheck.MIMEPNG
	mimeJPEG = imagecheck.MIMEJPEG
	mimeGIF  = imagecheck.MIMEGIF
	mimeWebP = imagecheck.MIMEWebP
)

type imageInfo struct {
	width  int
	height int
}

func inspectImage(mime string, data []byte) (imageInfo, error) {
	info, err := imagecheck.Inspect(mime, data, imagecheck.Limits{
		MaxRecords: maxImageRecords,
	})
	if err != nil {
		if errors.Is(err, imagecheck.ErrLimit) {
			return imageInfo{}, errBudget
		}
		return imageInfo{}, wrapExecution("%v", err)
	}
	return imageInfo{width: info.Width, height: info.Height}, nil
}

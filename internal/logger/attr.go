package logger

import (
	"log/slog"
	"strings"

	"github.com/dustin/go-humanize"
)

func ErrAttr(err error) slog.Attr {
	return slog.String("err", strings.TrimSpace(err.Error()))
}

func ErrorsAttr(errors ...error) slog.Attr {
	stringErrors := []string{}

	for _, err := range errors {
		stringErrors = append(stringErrors, strings.TrimSpace(err.Error()))
	}

	return slog.Any("errors", stringErrors)
}

func SizeAttrIEC[T int | int32 | int64 | uint64](b T, groupName ...string) slog.Attr {
	bytesUint64 := uint64(b)

	key := "size_iec"
	if len(groupName) > 0 {
		key = groupName[0]
	}

	return slog.Group(key,
		slog.String("pretty", humanize.IBytes(bytesUint64)),
		slog.Uint64("bytes", bytesUint64),
	)
}

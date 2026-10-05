package hibd

import (
	"encoding/json"
	"fmt"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/solev/hibernaut/internal/hibapi"
)

func marshalReply(reply hibapi.Response) ([]byte, error) {
	reply.V = hibapi.Version
	data, err := json.Marshal(reply)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, len(data)+1)
	for i := 0; i < len(data); {
		switch b := data[i]; {
		case b == '\\':
			switch data[i+1] {
			case 'b':
				out = append(out, `\u0008`...)
			case 'f':
				out = append(out, `\u000c`...)
			case 'n':
				out = append(out, `\u000a`...)
			case 'r':
				out = append(out, `\u000d`...)
			case 't':
				out = append(out, `\u0009`...)
			default:
				out = append(out, b, data[i+1])
			}
			i += 2
		case b == 0x7f:
			out = append(out, `\u007f`...)
			i++
		case b < utf8.RuneSelf:
			out = append(out, b)
			i++
		default:
			r, size := utf8.DecodeRune(data[i:])
			if r > 0xffff {
				hi, lo := utf16.EncodeRune(r)
				out = fmt.Appendf(out, `\u%04x\u%04x`, hi, lo)
			} else {
				out = fmt.Appendf(out, `\u%04x`, r)
			}
			i += size
		}
	}
	return append(out, '\n'), nil
}

func marshalBuildEvent(event hibapi.Event) ([]byte, error) {
	const limit = 64 << 10
	text := event.Text
	truncated := len(text) > limit
	if truncated {
		text = firstBytes(text, limit)
	}
	event.Text = text
	if truncated {
		event.Text += "…"
	}
	data, err := marshalReply(hibapi.Response{Event: &event})
	if err != nil || len(data) <= limit {
		return data, err
	}
	low, high := 0, len(text)
	for low < high {
		mid := (low + high + 1) / 2
		event.Text = firstBytes(text, mid) + "…"
		data, err = marshalReply(hibapi.Response{Event: &event})
		if err != nil {
			return nil, err
		}
		if len(data) <= limit {
			low = mid
		} else {
			high = mid - 1
		}
	}
	event.Text = firstBytes(text, low) + "…"
	return marshalReply(hibapi.Response{Event: &event})
}

func firstBytes(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	for limit > 0 && !utf8.RuneStart(text[limit]) {
		limit--
	}
	return text[:limit]
}

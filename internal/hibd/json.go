package hibd

import (
	"encoding/json"
	"fmt"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/solev/hibernaut/internal/hibapi"
)

const maxBuildLine = 64 << 10

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
	return marshalBoundedText(event.Text, "…", func(text string) hibapi.Response {
		event.Text = text
		return hibapi.Response{Event: &event}
	})
}

func marshalBoundedText(text, suffix string, reply func(string) hibapi.Response) ([]byte, error) {
	truncated := len(text) > maxBuildLine
	if truncated {
		text = firstBytes(text, maxBuildLine)
	}
	initial := text
	if truncated {
		initial += suffix
	}
	data, err := marshalReply(reply(initial))
	if err != nil || len(data) <= maxBuildLine {
		return data, err
	}
	low, high := 0, len(text)
	for low < high {
		mid := (low + high + 1) / 2
		data, err = marshalReply(reply(firstBytes(text, mid) + suffix))
		if err != nil {
			return nil, err
		}
		if len(data) <= maxBuildLine {
			low = mid
		} else {
			high = mid - 1
		}
	}
	return marshalReply(reply(firstBytes(text, low) + suffix))
}

func (a *App) buildReply(reply hibapi.Response) int {
	var data []byte
	var err error
	if reply.Error != "" {
		data, err = marshalBoundedText(reply.Error, "… Check hib image ls before trying again", func(text string) hibapi.Response {
			reply.Error = text
			return reply
		})
	} else {
		data, err = marshalReply(reply)
	}
	if err == nil && len(data) > maxBuildLine {
		return a.reply(hibapi.Response{Error: "build finished, but its reply exceeds 64 KiB. Check hib image ls before trying again"})
	}
	return a.writeReply(data, err, reply.Error != "")
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

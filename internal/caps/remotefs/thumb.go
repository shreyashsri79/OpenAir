package remotefs

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/jpeg"
	"os"
	"sync"

	// Registered for image.Decode. GIF and PNG cost nothing to support once
	// JPEG is here, and a photo folder holds all three.
	_ "image/gif"
	_ "image/png"

	"google.golang.org/protobuf/proto"

	"github.com/shreyashsri79/openair/internal/session"
	openairv1 "github.com/shreyashsri79/openair/internal/wire/openair/v1"
)

// Thumbnails, §11.3: generated on the source, so browsing a folder of RAW
// photos does not transfer them.
//
// §11.3 says sources SHOULD cache and MUST bound generation work, and both
// halves are load-bearing for a capability a peer can call in a loop. The
// bound here is on the *source* image rather than on time: a decoder given a
// 60000×60000 PNG allocates before it does anything a timeout could interrupt,
// so the pixel count is checked from the header first and the decode never
// starts.

const (
	// DefaultThumbDimension is the longer side when the client does not say.
	DefaultThumbDimension = 256

	// MaxThumbDimension bounds what a client may ask for. Past this it is not a
	// thumbnail, it is a transfer with extra steps -- and §11.2 already serves
	// those.
	MaxThumbDimension = 1024

	// maxSourcePixels bounds the image a source will decode. 40 megapixels is
	// larger than any camera in ordinary use and roughly 160 MB decoded, which
	// is the number that actually matters.
	maxSourcePixels = 40 << 20

	// thumbQuality is the JPEG quality of the output. High enough to look
	// right at 256px, low enough that a grid of them is a few hundred KB.
	thumbQuality = 80

	// thumbCacheEntries bounds the cache. Thumbnails are tens of KB, so this is
	// a few MB at most, and a directory being browsed fits in it.
	thumbCacheEntries = 128
)

// ThumbMIME is what this build renders. §11.3 leaves the format to the source.
const ThumbMIME = "image/jpeg"

// serveThumb answers a ThumbRequest.
func (c *Capability) serveThumb(ctx context.Context, st session.Stream, payload []byte) error {
	if !c.cfg.Thumbnails {
		return protoErr(session.CodeCapabilityUnavailable, nil, "this device does not generate thumbnails")
	}
	var req openairv1.ThumbRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return protoErr(session.CodeProtocolViolation, err, "malformed ThumbRequest")
	}
	full, err := c.resolve(req.GetPath())
	if err != nil {
		return err
	}

	dim := int(req.GetMaxDimension())
	switch {
	case dim <= 0:
		dim = DefaultThumbDimension
	case dim > MaxThumbDimension:
		dim = MaxThumbDimension
	}

	info, err := os.Stat(full)
	if err != nil {
		return fsErr(req.GetPath(), err)
	}
	key := fmt.Sprintf("%s|%d|%d|%d", full, info.ModTime().UnixNano(), info.Size(), dim)
	if img, ok := c.thumb.get(key); ok {
		return writeMessage(st, MsgThumbResponse, &openairv1.ThumbResponse{Mime: ThumbMIME, Image: img})
	}

	img, err := renderThumb(ctx, full, dim)
	if err != nil {
		return err
	}
	c.thumb.put(key, img)
	return writeMessage(st, MsgThumbResponse, &openairv1.ThumbResponse{Mime: ThumbMIME, Image: img})
}

// renderThumb decodes, scales and re-encodes one image.
func renderThumb(ctx context.Context, full string, dim int) ([]byte, error) {
	f, err := os.Open(full)
	if err != nil {
		return nil, fsErr(full, err)
	}
	defer f.Close()

	cfg, _, err := image.DecodeConfig(f)
	if err != nil {
		return nil, protoErr(session.CodeRejected, err, "this file is not an image this device can render")
	}
	if cfg.Width <= 0 || cfg.Height <= 0 {
		return nil, protoErr(session.CodeRejected, nil, "the image reports no size")
	}
	if int64(cfg.Width)*int64(cfg.Height) > maxSourcePixels {
		// Refused from the header, before the decoder allocates. This is the
		// "MUST bound generation work" of §11.3, and it has to happen here
		// because a decode in progress cannot be taken back.
		return nil, protoErr(session.CodeResourceExhausted, nil,
			"the image is %dx%d, past what this device will render", cfg.Width, cfg.Height)
	}
	if _, err := f.Seek(0, 0); err != nil {
		return nil, err
	}

	src, _, err := image.Decode(f)
	if err != nil {
		return nil, protoErr(session.CodeRejected, err, "this file is not an image this device can render")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, scale(src, dim), &jpeg.Options{Quality: thumbQuality}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// scale downsamples to fit dim on its longer side, averaging each destination
// pixel over the source rectangle it covers.
//
// A box filter rather than nearest-neighbour: nearest is a line of code shorter
// and produces visible aliasing on exactly the content thumbnails are for --
// text, fine detail, anything with an edge. It is also why this does not pull
// in golang.org/x/image, whose CatmullRom would look slightly better for a
// dependency and a build tag on every platform.
func scale(src image.Image, dim int) image.Image {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= dim && h <= dim {
		return src
	}
	dw, dh := w, h
	if w >= h {
		dw, dh = dim, max(1, h*dim/w)
	} else {
		dw, dh = max(1, w*dim/h), dim
	}

	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for y := 0; y < dh; y++ {
		sy0 := b.Min.Y + y*h/dh
		sy1 := max(sy0+1, b.Min.Y+(y+1)*h/dh)
		for x := 0; x < dw; x++ {
			sx0 := b.Min.X + x*w/dw
			sx1 := max(sx0+1, b.Min.X+(x+1)*w/dw)

			var r, g, bl, a, n uint64
			for sy := sy0; sy < sy1; sy++ {
				for sx := sx0; sx < sx1; sx++ {
					pr, pg, pb, pa := src.At(sx, sy).RGBA()
					r += uint64(pr)
					g += uint64(pg)
					bl += uint64(pb)
					a += uint64(pa)
					n++
				}
			}
			if n == 0 {
				continue
			}
			i := dst.PixOffset(x, y)
			dst.Pix[i+0] = uint8(r / n >> 8)
			dst.Pix[i+1] = uint8(g / n >> 8)
			dst.Pix[i+2] = uint8(bl / n >> 8)
			dst.Pix[i+3] = uint8(a / n >> 8)
		}
	}
	return dst
}

// thumbCache is a bounded cache keyed by path, mtime, size and dimension, so a
// file that changes is never served from it.
type thumbCache struct {
	mu    sync.Mutex
	items map[string][]byte
	order []string
}

func newThumbCache() *thumbCache {
	return &thumbCache{items: map[string][]byte{}}
}

func (c *thumbCache) get(key string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	b, ok := c.items[key]
	return b, ok
}

func (c *thumbCache) put(key string, b []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.items[key]; !exists {
		c.order = append(c.order, key)
	}
	c.items[key] = b
	for len(c.order) > thumbCacheEntries {
		delete(c.items, c.order[0])
		c.order = c.order[1:]
	}
}

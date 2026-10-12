package cc

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/modules/llm"
	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
	"github.com/alexberardi/jarvis-server/internal/modules/ocr"
)

// Images attached to client tool results (docs/cc/chat-images.md §10). A node's tool output may
// carry "image_attachments": [{"mime", "data" (base64), "label"}] (the desktop agent's
// desktop_read_screen does). They are lifted out of the output where results arrive, before
// anything records or forwards it, and shown to the live model as image parts of that tool
// message. The image is for the turn that asked for it only: conversation.commit drops it, so
// history keeps the label alone. Never stored: not in history, traces, transcripts, job
// payloads or logs. A live model without vision gets OCR text instead.

const (
	toolImagesKey = "image_attachments"
	toolMaxImages = 4
	// toolResultBodyMax is the continue / result-sink body cap: the usual body plus the images.
	toolResultBodyMax = chatBodyMax
	// toolImageNote follows each label in the tool output the model reads.
	toolImageNote = " (shown to you with this result; not kept after this turn)"
)

// ImageOCR reads text from images (the ocr module's in-process API): the fallback when the
// live model can't see images.
type ImageOCR interface {
	Recognize(ctx context.Context, imgs []ocr.Image, o ocr.Options, engines []string) ([]ocr.Reading, error)
}

// screenOCREngines are the local engines, best first; the LLM-vision engines are left out
// (that's what the live slot couldn't do).
var screenOCREngines = []string{"apple_vision", "rapidocr", "tesseract", "paddleocr", "easyocr"}

// liftToolImages takes the attached images out of a tool output: it returns the output with
// each image replaced by its label, and the images. Anything malformed is dropped with a note
// the model can read. Outputs without attachments come back unchanged.
func liftToolImages(out any) (any, []chatImage) {
	o, ok := out.(*pyjson.Object)
	if !ok {
		return out, nil
	}
	raw, ok := o.Get(toolImagesKey)
	if !ok {
		return out, nil
	}
	list, _ := raw.([]any)
	var imgs []chatImage
	labels := []any{}
	for i, e := range list {
		label := fmt.Sprintf("image %d", i+1)
		a, ok := e.(*pyjson.Object)
		if !ok {
			labels = append(labels, "[image dropped: not an object]")
			continue
		}
		if l, ok := a.Get("label"); ok {
			if s, ok := l.(string); ok && strings.TrimSpace(s) != "" {
				label = strings.TrimSpace(s)
			}
		}
		if len(imgs) == toolMaxImages {
			labels = append(labels, "[image dropped: more than 4 attached]")
			continue
		}
		data, _ := a.Get("data")
		img, why := decodeToolImage(data)
		if why != "" {
			labels = append(labels, "[image dropped: "+why+"]")
			continue
		}
		imgs = append(imgs, img)
		labels = append(labels, label+toolImageNote)
	}
	clean := pyjson.NewObject()
	for _, k := range o.Keys() {
		v, _ := o.Get(k)
		if k == toolImagesKey {
			v = labels
		}
		clean.Set(k, v)
	}
	return clean, imgs
}

// decodeToolImage validates one attachment's data (base64 or a data URL): at most
// chatMaxImageBytes of jpeg, png or webp by its bytes. It returns the reason when it can't.
func decodeToolImage(v any) (chatImage, string) {
	s, ok := v.(string)
	if !ok || s == "" {
		return chatImage{}, "no data"
	}
	if strings.HasPrefix(s, "data:") {
		if _, b64, ok := strings.Cut(s, ","); ok {
			s = b64
		}
	}
	if base64.StdEncoding.DecodedLen(len(s)) > chatMaxImageBytes+3 {
		return chatImage{}, "too large"
	}
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return chatImage{}, "not base64"
	}
	if len(b) > chatMaxImageBytes {
		return chatImage{}, "too large"
	}
	mime := sniffImage(b)
	if mime == "" {
		return chatImage{}, "not a jpeg, png or webp image"
	}
	return chatImage{mime: mime, dataURL: "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(b)}, ""
}

// toolMsg is a client tool result as a history message, with its images.
func toolMsg(tr toolResult) chatMsg {
	return chatMsg{Role: "tool", ToolCallID: tr.ToolCallID, Content: outputString(tr.Output), images: tr.images}
}

// prepareToolImages decides how the live model gets the results' images: as images when it
// has vision; otherwise as OCR text in the output (or a note that it can't see them).
func (m *Module) prepareToolImages(ctx context.Context, results []toolResult) []toolResult {
	has := false
	for _, r := range results {
		has = has || len(r.images) > 0
	}
	if !has || m.slotVision(ctx, llm.LabelLive) {
		return results
	}
	out := make([]toolResult, len(results))
	for i, r := range results {
		out[i] = r
		if len(r.images) == 0 {
			continue
		}
		out[i].images = nil
		o, ok := r.Output.(*pyjson.Object)
		if !ok {
			continue
		}
		o = copyObject(o)
		text, note := m.ocrText(ctx, r.images)
		if text != "" {
			o.Set("image_text", text)
		}
		o.Set("image_note", note)
		out[i].Output = o
	}
	return out
}

// ocrText reads the images' text with the best local engine that found any.
func (m *Module) ocrText(ctx context.Context, imgs []chatImage) (string, string) {
	const noVision = "The language model can't see images"
	if m.OCR == nil {
		return "", noVision + " and no OCR engine is available, so the image couldn't be read."
	}
	decoded, err := decodeChatImages(imgs)
	if err != nil {
		return "", noVision + "; the image couldn't be decoded."
	}
	in := make([]ocr.Image, len(decoded))
	for i, d := range decoded {
		in[i] = ocr.Image{Data: d.Data, ContentType: d.MIME}
	}
	readings, err := m.OCR.Recognize(ctx, in, ocr.Options{LanguageHints: []string{"en"}, Mode: "document"}, screenOCREngines)
	if err != nil {
		m.deps.Log.Warn("cc: tool image OCR failed", "err", redactErr(err))
		return "", noVision + " and OCR failed, so the image couldn't be read."
	}
	for _, rd := range readings {
		var parts []string
		for _, r := range rd.Results {
			if t := strings.TrimSpace(r.Text); t != "" {
				parts = append(parts, t)
			}
		}
		if len(parts) > 0 {
			return strings.Join(parts, "\n\n"), noVision + ": image_text is the text OCR (" + rd.Engine +
				") read from it; layout, colours and pictures are lost."
		}
	}
	return "", noVision + " and OCR found no text in it."
}

// dropToolImages removes images from tool messages (they are for the turn that asked for
// them only); the label in the content stays.
func dropToolImages(msgs []chatMsg) {
	for i := range msgs {
		if msgs[i].Role == "tool" && len(msgs[i].images) > 0 {
			msgs[i].images = nil
		}
	}
}

func copyObject(o *pyjson.Object) *pyjson.Object {
	c := pyjson.NewObject()
	for _, k := range o.Keys() {
		v, _ := o.Get(k)
		c.Set(k, v)
	}
	return c
}

// resultImages collects the results' images (the text path shows them with its formatting
// prompt, since it drops tool messages).
func resultImages(results []toolResult) []chatImage {
	var out []chatImage
	for _, r := range results {
		out = append(out, r.images...)
	}
	return out
}

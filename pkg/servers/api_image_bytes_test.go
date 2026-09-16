package servers

import (
	"bytes"
	"errors"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMissingImageAuthorizationFailsBeforeGeneration(t *testing.T) {
	api := &APIServer{}
	w := httptest.NewRecorder()
	if api.ensureImageAuthorization(w) || w.Code != http.StatusBadGateway || !strings.Contains(w.Body.String(), "image_auth_required") {
		t.Fatal("image authorization failed open")
	}
}

func TestImageDownloadFailureNeverReturnsAnAuthGatedURL(t *testing.T) {
	for _, format := range []string{"b64_json", "url", ""} {
		items, err := buildImageData("![image](https://designerappservice.officeapps.live.com/image?fileToken=private)", 1, "prompt", format, func(string) ([]byte, string, error) {
			return nil, "", errors.New("private upstream token must not leak")
		})
		if len(items) != 0 || err == nil || !strings.Contains(err.Error(), "Connect Microsoft account") || strings.Contains(err.Error(), "private") {
			t.Fatal("failed transfer misreported or leaked")
		}
	}
}

func TestDownloadedImageMustContainDecodableRasterBytes(t *testing.T) {
	var pngBytes bytes.Buffer
	_ = png.Encode(&pngBytes, image.NewRGBA(image.Rect(0, 0, 2, 2)))
	for _, data := range [][]byte{[]byte("<html>login</html>"), {}, pngBytes.Bytes()[:30]} {
		response := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"image/png"}}, Body: io.NopCloser(bytes.NewReader(data))}
		if _, _, err := readDownloadedImage(response); err == nil {
			t.Fatal("non-image or truncated response accepted")
		}
	}
	response := &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(pngBytes.Bytes()))}
	data, mime, err := readDownloadedImage(response)
	if err != nil || mime != "image/png" || !bytes.Equal(data, pngBytes.Bytes()) {
		t.Fatal("verified PNG not accepted")
	}
}

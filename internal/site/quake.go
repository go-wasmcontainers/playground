package site

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
)

// PinnedFile is a file of an image fetched from a release and pinned by
// size and sha256: a build refuses bytes that are not exactly those.
type PinnedFile struct {
	Path   string // in the image
	URL    string
	SHA256 string
	Size   int64
}

// FreepakTag is the go-quake1/freepak release the quake image ships. Its
// pak0.pak is LibreQuake's data: BSD-3-Clause, except progs.dat, which is
// GPL-2.0-or-later (GPL-NOTICE names the corresponding source). id
// Software's pak0.pak is never used here.
const FreepakTag = "v0.2.0"

func freepak(name, sum string, size int64) PinnedFile {
	return PinnedFile{
		Path:   "usr/share/quake/" + name,
		URL:    "https://github.com/go-quake1/freepak/releases/download/" + FreepakTag + "/" + name,
		SHA256: sum,
		Size:   size,
	}
}

// QuakeFiles are the freepak release's files the quake image carries: the
// pak and every licence file that goes with it.
var QuakeFiles = []PinnedFile{
	freepak("pak0.pak", "d08670904b5f6fe466135374b6c64e44a0c3a6dbee915c436f8f82bcb286652f", 36931086),
	freepak("COPYING", "60cb3ca34eb5ee733dd8af8803389d9a1fbcdbad9ff1765c7a92ff9b9ba95f0f", 1496),
	freepak("COPYING-GPL-2", "32b1062f7da84967e7019d01ab805935caa7ab7321a7ced0e30ebe75e5df1670", 17992),
	freepak("COPYING-LibreQuake", "c3426ae10fd52ab1a3c3d4de4f2fae60543403e3cfd09ae5dc2ec16ae4717d28", 1697),
	freepak("CREDITS-LibreQuake", "60b632851de3e5d02cb96ed158ff06700ced1c9fb6425f2d5d10509392021bd0", 3254),
	freepak("GPL-NOTICE", "e1b622d31e058f4b89d847968747012a3ec4ca502ae2afc654298a24681b5d99", 749),
	freepak("LibreProgsDeluxe.md", "9425d6c0c7fcfd0c5193efd6baefe285dc1e586b1dee89725821979cddb7b850", 664),
	freepak("MANIFEST", "82bb77d7f3f808367ee46a73635a8eaf61b023c12a48b3d61f16032f95ecc009", 48463),
	freepak("README-IMPORTANT-LICENCE-INFO", "121e4f2bbae888f442be76795c7e1b431bbfce9c7dd968407b4da969ccb50eae", 706),
	freepak("SHA256SUMS", "6be51f91bd573876085d4ee0efb7eaabf5a3bd1762d60f6169f2ff3154cf3a2d", 150),
}

// QuakeLicense is the image's licence file. The image combines the engine
// (go-quake1/engine, GPL-2.0-or-later) and LibreQuake's data; it is declared
// GPL-3.0-or-later as a whole, and the source is named.
const QuakeLicense = `quake: Quake in a wasmcontainers playground container

Licence of the image: GPL-3.0-or-later.

Source:
  engine   https://github.com/go-quake1/engine (cmd/quake-wctr), the release
           pinned by the playground's go.mod
  image    https://github.com/go-wasmcontainers/playground (images list in
           internal/site/quake.go)
  data     https://github.com/go-quake1/freepak, release ` + FreepakTag + `: /usr/share/quake/pak0.pak
           (sha256 d08670904b5f6fe466135374b6c64e44a0c3a6dbee915c436f8f82bcb286652f)

The data is LibreQuake's: BSD-3-Clause, except progs.dat, GPL-2.0-or-later.
Its licence files are next to the pak in /usr/share/quake/ (GPL-NOTICE names
the corresponding source of progs.dat). id Software's data is not part of
this image.
`

// ErrPinned is wrapped by FetchPinned for bytes that are not the pinned ones.
type ErrPinned struct {
	File PinnedFile
	Why  string
}

func (e *ErrPinned) Error() string {
	return fmt.Sprintf("pinned file %s (%s): %s", e.File.Path, e.File.URL, e.Why)
}

// FetchPinned downloads f with c (http.DefaultClient when nil) and returns
// its bytes, or an *ErrPinned when the answer is not 200, is longer than the
// pinned size, or does not have the pinned size and sha256. The body is
// never read past Size+1 bytes.
func FetchPinned(ctx context.Context, c *http.Client, f PinnedFile) ([]byte, error) {
	if c == nil {
		c = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.URL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, &ErrPinned{f, "HTTP " + resp.Status}
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, f.Size+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) != f.Size {
		return nil, &ErrPinned{f, fmt.Sprintf("%d bytes (or more), pinned %d", len(b), f.Size)}
	}
	if sum := sha256.Sum256(b); hex.EncodeToString(sum[:]) != f.SHA256 {
		return nil, &ErrPinned{f, fmt.Sprintf("sha256 %x, pinned %s", sum, f.SHA256)}
	}
	return b, nil
}

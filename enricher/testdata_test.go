package enricher

import (
	"encoding/hex"
	"testing"
)

// exifJPEGHex is a minimal JPEG (287 bytes) that carries an APP1/EXIF segment
// with Make=TestMake, Model=TestModel, CreateDate=2024:01:15 12:30:45. Generated
// by feeding a minimal valid JPEG to `exiftool -overwrite_original -Make=... -Model=... -CreateDate=...`.
// Embedding it as hex keeps binary blobs out of the repo.
const exifJPEGHex = "ffd8ffe100a44578696600004d4d002a000000080004010f0002000000090000003e011000020000000a0000004802130003000000010001000087690004000000010000005200000000546573744d616b650000546573744d6f64656c000004900000070000000430323332900400020000001400000088910100070000000401020300a001000300000001ffff000000000000323032343a30313a31352031323a33303a343500ffdb004300080606070605080707070909080a0c140d0c0b0b0c1912130f141d1a1f1e1d1a1c1c20242e2720222c231c1c2837292c30313434341f27393d38323c2e333432ffc0000b08000100010101110000ffc40014000100000000000000000000000000000000ffda0008010100003f00d2cfffd9"

// minimalPNGHex is a 67-byte valid PNG (signature + IHDR + IDAT + IEND).
// Used as a control image that carries no EXIF data.
const minimalPNGHex = "89504e470d0a1a0a0000000d49484452000000010000000108060000001f15c4890000000d4944415478da6300010000000500010d0a2db40000000049454e44ae426082"

const minimalPDF = "%PDF-1.4\n%%EOF\n"

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

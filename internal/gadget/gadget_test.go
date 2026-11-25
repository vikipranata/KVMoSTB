package gadget

import "testing"

// TestReportDescriptorsMatchReportLength parses the HID report descriptors
// and asserts the summed Input/Output bits equal the report sizes the code
// ships (8B keyboard, 6B mouse) and that collections balance. Catches
// descriptor/report_length drift at `go test` time, such as missing Report
// Size items that break all host input.
func TestReportDescriptorsMatchReportLength(t *testing.T) {
	cases := []struct {
		name    string
		desc    []byte
		in, out int // expected total input / output bits
	}{
		{"keyboard", keyboardReportDesc, 8 * 8, 8}, // 8-byte report, 1-byte LED output
		{"mouse", mouseReportDesc, 6 * 8, 0},       // 6-byte report, no output
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in, out, opens, closes := parseReportBits(c.desc)
			if in < 0 {
				t.Fatal("descriptor contains an unsupported item type")
			}
			if in != c.in {
				t.Errorf("input bits = %d, want %d (report_length drift)", in, c.in)
			}
			if out != c.out {
				t.Errorf("output bits = %d, want %d", out, c.out)
			}
			if opens != closes {
				t.Errorf("collections unbalanced: %d open vs %d close", opens, closes)
			}
		})
	}
}

// shortItemData gives the fixed data-byte length of every short item used by
// the two report descriptors (both use short items only). Extend this map if
// a new item type is introduced.
var shortItemData = map[byte]int{
	0x05: 1, 0x09: 1, 0x19: 1, 0x29: 1, // usage page / usage / usage min / usage max
	0x15: 1, 0x25: 1, 0x16: 2, 0x26: 2, // logical min / logical max
	0x75: 1, // report size
	0x95: 1, // report count
	0x81: 1, // input
	0x91: 1, // output
	0xa1: 1, // collection
	0xc0: 0, // end collection
}

// parseReportBits walks a short-item-only report descriptor and returns the
// total Input and Output bit counts plus collection open/close counts.
func parseReportBits(desc []byte) (in, out, opens, closes int) {
	size, count := 8, 1 // HID default values
	for i := 0; i < len(desc); {
		b := desc[i]
		n, ok := shortItemData[b]
		if !ok {
			return -1, -1, -1, -1
		}
		switch b {
		case 0x75:
			size = int(desc[i+1])
		case 0x95:
			count = int(desc[i+1])
		case 0x81:
			in += size * count
		case 0x91:
			out += size * count
		case 0xa1:
			opens++
		case 0xc0:
			closes++
		}
		i += 1 + n
	}
	return
}

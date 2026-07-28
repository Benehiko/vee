package vzfirstboot

import (
	"encoding/xml"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// parsePlist decodes an Apple XML plist into Go values: dict → map[string]any,
// array → []any, string/date/data → string, integer → int64, real → float64,
// true/false → bool. Only what the diskutil/hdiutil queries below need.
//
// A dedicated decoder is used rather than a plist dependency, and it is
// recursive rather than a flat key scanner: the queries walk nested
// containers → volumes → roles, where a flat scanner silently mixes levels.
func parsePlist(data []byte) (any, error) {
	dec := xml.NewDecoder(strings.NewReader(string(data)))
	for {
		tok, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("plist: no root value: %w", err)
		}
		start, ok := tok.(xml.StartElement)
		if !ok || start.Name.Local != "plist" {
			continue
		}
		return parsePlistValue(dec)
	}
}

// parsePlistValue decodes the next value element from dec.
func parsePlistValue(dec *xml.Decoder) (any, error) {
	for {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			return parsePlistElement(dec, t)
		case xml.EndElement:
			// End of the enclosing container: no value here.
			return nil, io.EOF
		}
	}
}

// parsePlistElement decodes the element start already consumed.
func parsePlistElement(dec *xml.Decoder, start xml.StartElement) (any, error) {
	switch start.Name.Local {
	case "dict":
		return parsePlistDict(dec)
	case "array":
		return parsePlistArray(dec)
	case "true":
		return true, dec.Skip()
	case "false":
		return false, dec.Skip()
	case "integer":
		text, err := plistText(dec, start)
		if err != nil {
			return nil, err
		}
		n, err := strconv.ParseInt(text, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("plist: integer %q: %w", text, err)
		}
		return n, nil
	case "real":
		text, err := plistText(dec, start)
		if err != nil {
			return nil, err
		}
		f, err := strconv.ParseFloat(text, 64)
		if err != nil {
			return nil, fmt.Errorf("plist: real %q: %w", text, err)
		}
		return f, nil
	default: // string, data, date
		return plistText(dec, start)
	}
}

// plistText returns the character data of a leaf element.
func plistText(dec *xml.Decoder, start xml.StartElement) (string, error) {
	var sb strings.Builder
	for {
		tok, err := dec.Token()
		if err != nil {
			return "", err
		}
		switch t := tok.(type) {
		case xml.CharData:
			sb.Write(t)
		case xml.EndElement:
			if t.Name.Local == start.Name.Local {
				return strings.TrimSpace(sb.String()), nil
			}
		}
	}
}

// parsePlistDict decodes a <dict> whose start element was consumed.
func parsePlistDict(dec *xml.Decoder) (map[string]any, error) {
	out := map[string]any{}
	var key string
	haveKey := false
	for {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local == "key" {
				key, err = plistText(dec, t)
				if err != nil {
					return nil, err
				}
				haveKey = true
				continue
			}
			val, err := parsePlistElement(dec, t)
			if err != nil {
				return nil, err
			}
			if haveKey {
				out[key] = val
				haveKey = false
			}
		case xml.EndElement:
			if t.Name.Local == "dict" {
				return out, nil
			}
		}
	}
}

// parsePlistArray decodes an <array> whose start element was consumed.
func parsePlistArray(dec *xml.Decoder) ([]any, error) {
	var out []any
	for {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			val, err := parsePlistElement(dec, t)
			if err != nil {
				return nil, err
			}
			out = append(out, val)
		case xml.EndElement:
			if t.Name.Local == "array" {
				return out, nil
			}
		}
	}
}

// dictOf returns v as a plist dict, or nil.
func dictOf(v any) map[string]any {
	d, _ := v.(map[string]any)
	return d
}

// arrayOf returns v as a plist array, or nil.
func arrayOf(v any) []any {
	a, _ := v.([]any)
	return a
}

// stringOf returns v as a plist string, or "".
func stringOf(v any) string {
	s, _ := v.(string)
	return s
}

// attachedDisk names the device nodes of an attached (unmounted) image.
type attachedDisk struct {
	// WholeDisk is the image's whole-disk node (e.g. "disk4"); detaching it
	// releases the image.
	WholeDisk string
	// Container is the Apple_APFS partition (e.g. "disk4s2").
	Container string
	// Data is the APFS volume with role Data (e.g. "disk7s5") — everything
	// writable in a macOS install lives there. Resolved separately, because
	// an attach that does not mount does not report volume roles.
	Data string
}

// devicesFromAttach picks the whole-disk node and the APFS container
// partition out of the system-entities list that `hdiutil attach -plist`
// (and `diskutil image attach -plist`) print.
func devicesFromAttach(root any) attachedDisk {
	var got attachedDisk
	for _, e := range arrayOf(dictOf(root)["system-entities"]) {
		entity := dictOf(e)
		dev := strings.TrimPrefix(stringOf(entity["dev-entry"]), "/dev/")
		if dev == "" {
			continue
		}
		switch stringOf(entity["content-hint"]) {
		case "GUID_partition_scheme":
			got.WholeDisk = dev
		case "Apple_APFS":
			got.Container = dev
		}
		// Some attach outputs already carry volume roles; take Data if so.
		if stringOf(entity["role"]) == "Data" {
			got.Data = dev
		}
	}
	if got.WholeDisk == "" {
		// Fall back to the shortest device node: the whole disk is always a
		// prefix of its partitions (disk4 vs disk4s2).
		for _, e := range arrayOf(dictOf(root)["system-entities"]) {
			dev := strings.TrimPrefix(stringOf(dictOf(e)["dev-entry"]), "/dev/")
			if dev == "" {
				continue
			}
			if got.WholeDisk == "" || len(dev) < len(got.WholeDisk) {
				got.WholeDisk = dev
			}
		}
	}
	return got
}

// dataVolumeForContainer finds the Data-role volume of the APFS container
// backed by physical store containerDev, from `diskutil apfs list -plist`
// output. The apfs verb exists on every macOS version vee supports, unlike
// the macOS 26+ `diskutil image` verb.
func dataVolumeForContainer(root any, containerDev string) (string, error) {
	for _, c := range arrayOf(dictOf(root)["Containers"]) {
		container := dictOf(c)
		if !containerHasStore(container, containerDev) {
			continue
		}
		for _, v := range arrayOf(container["Volumes"]) {
			vol := dictOf(v)
			for _, role := range arrayOf(vol["Roles"]) {
				if stringOf(role) == "Data" {
					dev := stringOf(vol["DeviceIdentifier"])
					if dev == "" {
						return "", fmt.Errorf("APFS Data volume has no device identifier")
					}
					return dev, nil
				}
			}
		}
		return "", fmt.Errorf("APFS container %s has no Data-role volume — is the guest actually installed?", containerDev)
	}
	return "", fmt.Errorf("no APFS container found for %s", containerDev)
}

// containerHasStore reports whether an APFS container entry is backed by dev.
func containerHasStore(container map[string]any, dev string) bool {
	if stringOf(container["DesignatedPhysicalStore"]) == dev {
		return true
	}
	for _, s := range arrayOf(container["PhysicalStores"]) {
		if stringOf(dictOf(s)["DeviceIdentifier"]) == dev {
			return true
		}
	}
	return false
}

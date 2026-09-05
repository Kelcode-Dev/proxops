package schema

import "fmt"

// DiskBytes converts a human quantity ("50GiB") to a PVE disk size in bytes.
func DiskBytes(q string) (int64, error) {
	return ParseBytes(q)
}

// ParseState validates a desired power state string.
func ParseState(s string) (string, error) {
	switch s {
	case "started", "stopped":
		return s, nil
	case "":
		return "started", nil
	default:
		return "", fmt.Errorf("invalid state %q (want started|stopped)", s)
	}
}

package template

import "io/fs"

// goModeToPosix converts Go's fs.FileMode to POSIX-style st_mode as an
// unsigned integer. It mirrors what Python's os.stat().st_mode returns on
// Linux, so hashes that include str(st_mode) align across SDKs.
func goModeToPosix(m fs.FileMode) uint32 {
	posix := uint32(m.Perm())

	switch {
	case m&fs.ModeSymlink != 0:
		posix |= 0o120000
	case m.IsDir():
		posix |= 0o040000
	case m&fs.ModeSocket != 0:
		posix |= 0o140000
	case m&fs.ModeNamedPipe != 0:
		posix |= 0o010000
	case m&fs.ModeCharDevice != 0:
		posix |= 0o020000
	case m&fs.ModeDevice != 0:
		posix |= 0o060000
	default:
		posix |= 0o100000
	}

	if m&fs.ModeSetuid != 0 {
		posix |= 0o4000
	}
	if m&fs.ModeSetgid != 0 {
		posix |= 0o2000
	}
	if m&fs.ModeSticky != 0 {
		posix |= 0o1000
	}
	return posix
}

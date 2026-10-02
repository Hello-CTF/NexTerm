package mount

func validWindowsDrive(value string) bool {
	return len(value) == 2 && value[1] == ':' && isASCIILetter(value[0])
}

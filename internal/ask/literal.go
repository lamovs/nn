package ask

// Literal renders model text or note metadata as one line of inert prose:
// nothing in it can form a link, a source marker, or a control sequence.
func Literal(text string) string {
	return literal(text)
}

package explorer

type Directory struct {
	name      string
	files     []File
	Directory []Directory
}

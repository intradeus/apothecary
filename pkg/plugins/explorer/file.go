package explorer

import "time"

type File struct {
	name      string
	extension string
	content   []byte
	created   time.Time
	modified  time.Time
}

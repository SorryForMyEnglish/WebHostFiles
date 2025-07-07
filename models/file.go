package models

type File struct {
	ID          int64
	UserID      int64
	LocalName   string
	StorageName string
	Link        string
	Notify      bool
	Size        int64
	CreatedAt   string
}

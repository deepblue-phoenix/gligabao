// Package imagestore keeps the bytes of image artifacts. The database only tracks them (see gb_image_artifacts.path),
// so images never go into it. All other code talks to the Store interface; where the bytes really live (a directory of
// the file system, or an Amazon S3 bucket) is decided once, in the configuration (image_store, see package config).
//
// An image is named after the SHA-256 of its content and lives under its dataset: "<dataset id>/<sha256>.<ext>". That
// relative path (always with forward slashes) is what the database stores, so the directory or the bucket prefix can
// be moved. In an S3 bucket every user has a directory of their own: the object key is "<prefix>/<user>/<path>". Every
// operation on an image names its owner and the store can only reach that owner's directory (see S3), so a user can
// never read, write or delete the images of another one. Storing the same bytes again in a dataset is a no-op that returns the same path.
package imagestore

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strconv"
	"strings"
)

// Object is one stored image file.
type Object struct {
	Owner string // the user the image belongs to; "" if the store does not keep it (FS)
	Path  string // relative to the owner's directory, with forward slashes
	Size  int64
}

// Store is where image files are kept. It is safe for concurrent use. owner is the id of the user the image belongs to
// (required); an S3 store reaches only that user's directory.
type Store interface {
	// Put writes data at rel of owner. created is false if it already existed (it is then left as it is). A reader never
	// sees a partial object.
	Put(ctx context.Context,
		owner,
		rel string,
		data []byte) (created bool, err error)
	// Read returns the bytes at rel of owner. It returns an error wrapping ErrNotFound if there is no such object.
	Read(ctx context.Context,
		owner,
		rel string) ([]byte, error)
	// Remove deletes rel of owner. An object that is already gone is not an error.
	Remove(ctx context.Context,
		owner,
		rel string) error
	// List returns every stored object of every user. It is for operators (admin statistics, export), never for the
	// requests of a user. A store whose root does not exist yet returns an error wrapping ErrNotFound.
	List(ctx context.Context) ([]Object, error)
	// Location describes where the store is, without secrets ("/data/x.images", "s3://bucket/prefix").
	Location() string
}

// ErrNotFound is wrapped by the errors of Read and List for what is not there.
var ErrNotFound = errors.New("image store: not found")

var extensions = map[string]string{
	"image/jpeg": "jpg",
	"image/png":  "png",
	"image/gif":  "gif",
	"image/webp": "webp",
}

// Path returns the relative path of an image of the dataset, given its media type and hex SHA-256.
func Path(datasetID int64,
	mediaType,
	sha256 string) (string, error) {
	ext, ok := extensions[mediaType]
	if !ok {
		return "", fmt.Errorf("image store: unsupported media type %q", mediaType)
	}
	if len(sha256) != 64 || strings.Trim(sha256, "0123456789abcdef") != "" {
		return "", fmt.Errorf("image store: bad SHA-256 %q", sha256)
	}
	return path.Join(strconv.FormatInt(datasetID, 10), sha256+"."+ext), nil
}

// Save stores data, of the given media type and hex SHA-256, for the dataset of owner and returns its path. created is false if
// it was there already (same dataset, same content): a caller undoing its work must not Remove it then.
func Save(ctx context.Context,
	s Store,
	owner string,
	datasetID int64,
	mediaType,
	sha256 string,
	data []byte) (rel string, created bool, err error) {
	rel, err = Path(datasetID,
		mediaType,
		sha256)
	if err != nil {
		return "", false, err
	}
	created, err = s.Put(ctx,
		owner,
		rel,
		data)
	if err != nil {
		return "", false, err
	}
	return rel, created, nil
}

// Resolve returns store, or, if it is nil, a file system store in dir (nil if dir is empty too).
func Resolve(store Store,
	dir string) Store {
	switch {
	case store != nil:
		return store
	case dir != "":
		return NewFS(dir)
	default:
		return nil
	}
}

// cleanPath checks that rel is a plain relative path that cannot climb out of the root.
func cleanPath(rel string) (string, error) {
	clean := path.Clean("/" + rel)[1:] // rooted first, so ".." cannot climb out
	if clean == "" || clean != rel {
		return "", fmt.Errorf("image store: bad path %q", rel)
	}
	return clean, nil
}

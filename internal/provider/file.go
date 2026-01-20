package provider

import (
	"context"
	"os"

	"modern_reverse_proxy/internal/config"
)

const FilePriority = 50

type File struct {
	Path string
}

func NewFileProvider(path string) *File {
	return &File{Path: path}
}

func (f *File) Name() string {
	return "file"
}

func (f *File) Priority() int {
	return FilePriority
}

func (f *File) Load(ctx context.Context) (*config.Config, error) {
	_ = ctx
	if f == nil || f.Path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(f.Path)
	if err != nil {
		return nil, err
	}
	return config.ParseJSON(data)
}

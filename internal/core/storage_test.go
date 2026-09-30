package core

import "testing"

func TestFileCategory(t *testing.T) {
	cases := map[string]string{
		"/Users/test/Movies/film.MOV":                        "video",
		"/Users/test/Documents/report.pdf":                   "documents",
		"/Users/test/project/node_modules/package/photo.png": "development",
		"/Users/test/archive.zip":                            "archives",
		"/Users/test/unknown":                                "other",
	}
	for path, want := range cases {
		if got := FileCategory(path); got != want {
			t.Errorf("%s: got %s, want %s", path, got, want)
		}
	}
}

func TestStorageCapacity(t *testing.T) {
	info, err := StorageInfo()
	if err != nil {
		t.Fatal(err)
	}
	if info.Total == 0 || info.Available > info.Total {
		t.Fatalf("invalid capacity: %+v", info)
	}
}

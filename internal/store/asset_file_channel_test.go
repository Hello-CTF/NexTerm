package store

import "testing"

func TestAssetFileChannelOption(t *testing.T) {
	for raw, want := range map[string]bool{
		`{"fileChannel":"sftp"}`: true,
		`{"fileChannel":"auto"}`: false,
		`{}`:                     false,
		`{"fileChannel":7}`:      false,
		`not json`:               false,
	} {
		if got := AssetFileChannelSFTP(raw); got != want {
			t.Errorf("AssetFileChannelSFTP(%s) = %v, want %v", raw, got, want)
		}
	}
	for raw, valid := range map[string]bool{
		`{"fileChannel":"sftp"}`:   true,
		`{"fileChannel":"auto"}`:   true,
		`{"fileChannel":null}`:     true,
		`{}`:                       true,
		`{"fileChannel":"helper"}`: false,
		`{"fileChannel":true}`:     false,
	} {
		if err := validateAssetFileChannel(raw); (err == nil) != valid {
			t.Errorf("validateAssetFileChannel(%s) = %v, want valid %v", raw, err, valid)
		}
	}
}

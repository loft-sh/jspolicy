package compress

import (
	"bytes"
	"compress/gzip"
	"io/ioutil"
)

// Compress gzips a string and base64 encodes it
func Compress(s string) ([]byte, error) {
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)

	_, err := gz.Write([]byte(s))
	if err != nil {
		return nil, err
	}

	err = gz.Flush()
	if err != nil {
		return nil, err
	}

	err = gz.Close()
	if err != nil {
		return nil, err
	}

	return b.Bytes(), nil
}

// Decompress decompresses a string
func Decompress(s []byte) ([]byte, error) {
	rdata := bytes.NewReader(s)
	r, err := gzip.NewReader(rdata)
	if err != nil {
		return nil, err
	}

	decompressed, err := ioutil.ReadAll(r)
	if err != nil {
		return nil, err
	}

	return decompressed, nil
}

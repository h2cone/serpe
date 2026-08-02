package sdkhttp

// RawJSON returns a successful SDK response's preserved wire JSON.
func RawJSON[T interface{ RawJSON() string }](response T, err error) ([]byte, error) {
	if err != nil {
		return nil, err
	}
	return []byte(response.RawJSON()), nil
}

// StartStream validates SDK stream setup and exposes its close operation.
func StartStream[T interface {
	Err() error
	Close() error
}](stream T) (func() error, error) {
	if err := stream.Err(); err != nil {
		_ = stream.Close()
		return nil, err
	}
	return stream.Close, nil
}

package proxy

import "net/http"

type ResponseRecorder struct {
	writer        http.ResponseWriter
	status        int
	bytesWritten  int64
	wroteHeader   bool
	errorCategory string
}

type errorCategoryWriter interface {
	SetErrorCategory(string)
}

func NewResponseRecorder(w http.ResponseWriter) *ResponseRecorder {
	return &ResponseRecorder{writer: w, status: http.StatusOK}
}

func (r *ResponseRecorder) Header() http.Header {
	return r.writer.Header()
}

func (r *ResponseRecorder) WriteHeader(status int) {
	if !r.wroteHeader {
		r.status = status
		r.wroteHeader = true
	}
	r.writer.WriteHeader(status)
}

func (r *ResponseRecorder) Write(data []byte) (int, error) {
	if !r.wroteHeader {
		r.WriteHeader(http.StatusOK)
	}
	n, err := r.writer.Write(data)
	r.bytesWritten += int64(n)
	return n, err
}

func (r *ResponseRecorder) Status() int {
	return r.status
}

func (r *ResponseRecorder) BytesWritten() int64 {
	return r.bytesWritten
}

func (r *ResponseRecorder) SetErrorCategory(category string) {
	r.errorCategory = category
}

func (r *ResponseRecorder) ErrorCategory() string {
	return r.errorCategory
}

func (r *ResponseRecorder) WroteHeader() bool {
	return r.wroteHeader
}

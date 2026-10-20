package execbatch

import "context"

type AdmissionFunc func(context.Context, func() error) error

type admissionContextKey struct{}

func WithAdmission(ctx context.Context, admit AdmissionFunc) context.Context {
	if admit == nil {
		return ctx
	}
	return context.WithValue(ctx, admissionContextKey{}, admit)
}

func admit(ctx context.Context, fn func() error) error {
	admit, _ := ctx.Value(admissionContextKey{}).(AdmissionFunc)
	if admit == nil {
		return fn()
	}
	return admit(ctx, fn)
}

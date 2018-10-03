package amazon

import "github.com/pkg/errors"

var (
	ErrReadVPC        = errors.New("aws: can't read vpc info")
	ErrCreateVPC      = errors.New("aws: create vpc")
	ErrAuthorization  = errors.New("aws: authorization")
	ErrCreateSubnet   = errors.New("aws: create subnet")
	ErrCreateInstance = errors.New("aws: create instance")
	ErrNoPublicIP     = errors.New("aws: no public IP assigned")
)

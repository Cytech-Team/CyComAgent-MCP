package remotejobs

import "errors"

var ErrJobManagerRequired = errors.New("remote structured execution requires a Job Manager")

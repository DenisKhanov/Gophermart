package customerrors

import "errors"

var ErrUserAlreadyTaken = errors.New("username already taken")
var ErrSaveNewUser = errors.New("it is not possible to save the user to the database")
var ErrUnauthorizedUser = errors.New("login or password uncorrected")
var ErrTokenIsNotValid = errors.New("token is not valid")
var ErrOrderNumber = errors.New("invalid order number format")
var ErrUserOrderExists = errors.New("the order number has already been uploaded by this user")
var ErrAnotherUserOrderExists = errors.New("the order number has already been uploaded by another user")
var ErrAccessingDB = errors.New("error accessing the database")
var ErrUserHasNoOrders = errors.New("this user does not have any orders")
var ErrUserHasNoWithdrawals = errors.New("this user does not have any withdrawals")
var ErrNotEnoughFunds = errors.New("there are not enough funds in the bonus account to be debited")

package dropapi

type PrivateTesterInput struct {
	App   string `json:"app"`
	Email string `json:"email"`
}

type PrivateAccessInput struct {
	App string `json:"app"`
}

package dropapi

type CreditViewInput struct {
	Account string `json:"account,omitempty"`
	Before  int64  `json:"before,omitempty"`
}

type CreditAdjustment struct {
	Account      string `json:"account"`
	Microcredits int64  `json:"microcredits"`
}

type CreditSettings struct {
	EuroMicrosPerCredit int64 `db:"euro_micros_per_credit" json:"euro_micros_per_credit"`
	Pack                int64 `db:"pack" json:"pack"`
	Cap                 int64 `db:"cap" json:"cap"`
	Starting            int64 `db:"starting" json:"starting"`
	MarkupPPM           int64 `db:"markup_ppm" json:"markup_ppm"`
	EURPerUSDPPM        int64 `db:"eur_per_usd_ppm" json:"eur_per_usd_ppm"`
}

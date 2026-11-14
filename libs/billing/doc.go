// Package billing keeps the API keys, their credit balances and the
// customer accounts, as JSON blobs in the billing container:
//
//	keys/<id>.json                       Key, where id = hex sha256 of the API key
//	jobs/<job>.json                      Job, who submitted a job (read by the consumer)
//	accounts/<account>.json              Account
//	accounts/<account>/keys/<id>         empty: index of the account's keys
//	emails/<sha256(email)>.json          the account of an email address
//	freecredits/<hmac(email)>.json       the address has had its free credits
//	sessions/<sha256(token)>.json        Session
//	logintokens/<sha256(token)>.json     a magic-link token
//	ratelimits/<kind>/<value>/<start>    a fixed-window counter
//
// Keys, session tokens and login tokens are never stored, only their
// hashes. Balance changes read the key blob with its ETag and write it back
// with If-Match, retrying when another request changed it in between, so two
// requests can't spend the same credits. Blobs that must be unique or used
// once are created with If-None-Match: * or deleted with If-Match.
package billing

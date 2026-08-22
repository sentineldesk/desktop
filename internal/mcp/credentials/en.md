# Credential vocabulary — English

Words that NAME a secret, phrases that introduce one, and values that only look
like one. Merged with every other file in this directory, so a language adds
terms rather than replacing them — the desktop has no idea what language the
machine it is watching was configured in, and does not need one.

## What this is not

It is not a list of secrets, and it is not a list of words that trigger a
warning on their own. That distinction is the whole reason a large dictionary
helps here instead of ruining the detector.

These terms are the LEFT side of an expression. `clave` in a sentence is a
person talking; `clave = Tr0ub4dor3` is a credential. The structure — a name, a
separator, a value of some length that is not a placeholder — is what keeps the
false positive rate low enough that people keep reading the warnings. A
dictionary matched on its own would fire on documentation, on error messages
and on ordinary conversation, and within a day the banner would mean nothing.

So this file can grow as large as anybody likes. Every term added widens what
is caught without widening what is guessed at, because none of them fires
alone.

One term per line or comma-separated; `#` starts a comment.

## names

Anything that appears to the left of `=` or `:` immediately before a value. A
prefix is allowed, so `db_password`, `MYSQL_ROOT_PASSWORD` and `app.secret` all
match `password` and `secret`.

password, passwords, passwd, pass, pwd, passphrase, pass phrase
secret, secrets, secret key, secretkey, client secret, clientsecret, app secret
api key, apikey, api token, apitoken, api secret
access token, accesstoken, auth token, authtoken, authorization token
bearer token, id token, refresh token, session token, session key
credential, credentials, private key, privatekey, public key id
access key, secret access key, master key, encryption key, signing key
security token, service key, service account key, deploy key
license key, activation key, product key, registration key
connection string, database url, db url, dsn
webhook secret, shared secret, hmac key, signing secret
recovery code, backup code, otp secret, totp secret, mfa secret
seed phrase, mnemonic, keystore password, truststore password
smtp password, ftp password, ssh password, sudo password, root password
admin password, user password, login password, vault password
token, auth, authorization

## phrases

Prose that introduces a secret, for the case that regularly matters most: a
person typing or pasting into a terminal the agent is reading, or a chat message
in a document on screen. Matched with a shorter minimum length than an
assignment, because a value quoted after a phrase is usually the whole of it.

the password is, password is, my password is, the pass is
the passphrase is, passphrase is
the key is, the secret is, secret is, the token is, token is
credentials are, the credentials are, log in with, login with, sign in with
use the password, use password, with the password
username and password, user and password

## placeholders

Values that match everything above and mean nothing. Without these, every
example configuration in the container reports a credential, and the third false
alarm is when somebody stops reading the fourth.

changeme, change me, change_me, change-me, changethis, change this
your password, yourpassword, your_password, your-password, your secret
your key, yourkey, your_key, my password, mypassword, my_password
placeholder, example, sample, dummy, test, testing, demo
redacted, hidden, masked, censored, removed, omitted
notset, not set, unset, undefined, none, null, nil, empty, blank
todo, fixme, tbd, pending, required, optional
insert here, fill in, replace this, set this, enter password
password, passwd, secret, key, token, credentials

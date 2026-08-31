# Extra CA trust

Only needed if your network does TLS interception. Symptom: Bifrost exits at boot with

    tls: failed to verify certificate: x509: certificate signed by unknown authority

Corporate proxies re-sign TLS with their own root, which a container does not trust. This
affects Bifrost's own startup fetches and, more importantly, the plugin's calls to Okta.

To fix it, drop a PEM bundle here and point `EXTRA_CA_BUNDLE` at it in `.env`:

    EXTRA_CA_BUNDLE=/certs/ca-bundle.crt

The bundle must contain your proxy's root **and** the normal public roots, because it
replaces the default trust store rather than adding to it. To build one:

    # your proxy's root, from the macOS system keychain
    security find-certificate -a -c "<Your Proxy Root CA name>" \
      -p /Library/Keychains/System.keychain > proxy-root.pem

    # the public roots the image already ships
    cid=$(docker create maximhq/bifrost:latest)
    docker cp "$cid:/etc/ssl/certs/ca-certificates.crt" base.crt
    docker rm -f "$cid"

    cat base.crt proxy-root.pem > ca-bundle.crt

To find the name of the CA intercepting you:

    echo | openssl s_client -connect getbifrost.ai:443 -servername getbifrost.ai 2>/dev/null \
      | grep " s:"

Everything in this directory except this file is gitignored. Do not commit a bundle: it
identifies your employer's network, and it is specific to your machine.

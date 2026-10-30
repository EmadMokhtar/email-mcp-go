# Postfix for the interop test. Postfix has no official image, so this one is
# built from the Debian image with the Debian postfix package.
#
# debian:bookworm-slim
FROM debian:bookworm-slim@sha256:99c0b25c46d0214d5a2091092e7908907dcaecd6a962cde0ac700acee222b6f3

# Without the debconf answer, the install would ask how to set Postfix up.
RUN echo "postfix postfix/main_mailer_type select No configuration" | debconf-set-selections \
 && apt-get update \
 && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends postfix \
 && rm -rf /var/lib/apt/lists/*

COPY main.cf /etc/postfix/main.cf

# master.cf of the package, changed:
#   - no service runs in a chroot, so each one can find "dovecot" by name;
#   - no SMTP on port 25: this server only takes mail from logged-in users;
#   - submission on 587 (STARTTLS) and on 465 (implicit TLS). Both need a login,
#     and a login needs TLS first (smtpd_tls_auth_only in main.cf).
RUN postconf -F '*/*/chroot = n' \
 && postconf -MX smtp/inet \
 && postconf -M submission/inet='submission inet n - n - - smtpd' \
 && postconf -P 'submission/inet/syslog_name=postfix/submission' \
                'submission/inet/smtpd_tls_security_level=encrypt' \
                'submission/inet/smtpd_sasl_auth_enable=yes' \
                'submission/inet/smtpd_client_restrictions=permit_sasl_authenticated,reject' \
 && postconf -M submissions/inet='submissions inet n - n - - smtpd' \
 && postconf -P 'submissions/inet/syslog_name=postfix/submissions' \
                'submissions/inet/smtpd_tls_wrappermode=yes' \
                'submissions/inet/smtpd_sasl_auth_enable=yes' \
                'submissions/inet/smtpd_client_restrictions=permit_sasl_authenticated,reject' \
 && postfix check

EXPOSE 587 465

# start-fg keeps the master process in the foreground, as a container needs.
CMD ["postfix", "start-fg"]

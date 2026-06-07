# Build-from-source fixture for the platform builder (#14, ADR-0003). A tiny
# static site served by busybox httpd on :8080 — enough to exercise
# build → cache → boot end-to-end without a language toolchain, so builds stay
# fast. The run contract runs this as a non-root (uid 1000), all-caps-dropped
# user with a read-only rootfs; httpd only reads /www and binds a non-privileged
# port, so it needs none of that.
FROM busybox:1.37
COPY index.html /www/index.html
EXPOSE 8080
CMD ["httpd", "-f", "-p", "8080", "-h", "/www"]

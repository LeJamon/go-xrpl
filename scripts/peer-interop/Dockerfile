FROM ubuntu:22.04

ADD --checksum=sha256:cae8ce3b9bc9451b19975c890714ba789d2004987cdfff9cbd55522c612c6f26 https://packages.xrplf.org/repository/deb-stable/pool/x/xrpld/xrpld_3.4.1-1_amd64.deb /tmp/xrpld.deb
RUN test "$(dpkg --print-architecture)" = amd64 \
    && dpkg-deb -x /tmp/xrpld.deb / \
    && printf '%s\n' 'fb8430dfdbee9d8016818dab22881e48a125598beebf9b5b8c90ca7fdba1faaa  /usr/bin/xrpld' | sha256sum --check \
    && rm /tmp/xrpld.deb \
    && mkdir -p /var/lib/xrpld/db /var/log/xrpld

ENTRYPOINT ["/usr/bin/xrpld", "--conf", "/etc/xrpld/xrpld.cfg", "--fg"]

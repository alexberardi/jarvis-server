# The L1 lane's PBX: Asterisk 22 (the Asterisk FreePBX 17 runs), pinned by digest, plus the
# English core prompts FreePBX installs (the image ships none) and the lane's stand-in for
# FreePBX's Responsive Firewall (lane-firewall). Configuration is bind-mounted by
# scripts/sip-lane, rendered from test/sip/asterisk/*.conf. docs/cc/phone-sip.md §5.
FROM andrius/asterisk:22@sha256:d6333b8b331563c5887b98b12da9371ef48317e657d17d81770fe085f335041d

ADD --checksum=sha256:83ec602fb1f2cb06a5194a95f855b84bae35d49d5bdb6fbd9274d5d1a5d16b0e \
    https://downloads.asterisk.org/pub/telephony/sounds/releases/asterisk-core-sounds-en-ulaw-1.6.1.tar.gz /tmp/sounds.tgz
RUN mkdir -p /var/lib/asterisk/sounds/en /var/lib/asterisk/lane \
 && tar xzf /tmp/sounds.tgz -C /var/lib/asterisk/sounds/en && rm /tmp/sounds.tgz
COPY lane-firewall lane-entrypoint /usr/local/bin/
ENTRYPOINT ["/usr/local/bin/lane-entrypoint"]
CMD ["/usr/sbin/asterisk", "-fnvvv", "-U", "asterisk"]

// The prebuilt BI app fetches the sibling gold prefix the Site mounts at /data (ADR-0139).
fetch('/data/roundtrip-site.txt').then((r) => r.text()).then((t) => console.log('gold:', t));

const fs = require('fs')
const zlib = require('zlib');

const payload = fs.readFileSync('./dist/bundle.js');
const jsPolicy = fs.readFileSync('./templates/jspolicy.yaml');
const jsPolicyBundle = fs.readFileSync('./templates/jspolicybundle.yaml');

zlib.gzip(payload, function (_, result) { 
    const compressedPayload = result.toString('base64');

    // write the final files
    fs.writeFileSync("./manifests/jspolicy.yaml", jsPolicy);
    fs.writeFileSync("./manifests/jspolicybundle.yaml", jsPolicyBundle.toString("ascii").replace("##BUNDLE##", compressedPayload));
});

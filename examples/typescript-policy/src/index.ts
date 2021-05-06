import {denyPrivilegedPod} from "./policy";

const errors = denyPrivilegedPod(request);
if (errors.length > 0) {
    deny(errors.join(", "))
}

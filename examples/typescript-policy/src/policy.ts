import {V1AdmissionRequest} from '@jspolicy/types';
import {V1Pod} from "@kubernetes/client-node";

export function denyPrivilegedPod(req: V1AdmissionRequest): string[] {
    const pod = req.object as V1Pod;
    const errors: string[] = [];

    pod?.spec?.containers?.forEach((container, index) => {
        if (container.securityContext?.privileged) {
            errors.push(`spec.containers[${index}].securityContext.privileged is not allowed`)
        }
    })

    pod?.spec?.initContainers?.forEach((container, index) => {
        if (container.securityContext?.privileged) {
            errors.push(`spec.initContainers[${index}].securityContext.privileged is not allowed`)
        }
    })

    return errors;
}


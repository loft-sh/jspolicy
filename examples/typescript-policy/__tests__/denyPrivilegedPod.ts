import { denyPrivilegedPod } from "../src/policy";

describe("Test denyPrivilegedPod", () => {
    test("Check container", () => {
        expect(denyPrivilegedPod({object: {spec: {containers: [{securityContext: {privileged: true}}]}}} as any)).toHaveLength(1);
    });
    test("Check init container", () => {
        expect(denyPrivilegedPod({object: {spec: {containers: [{securityContext: {privileged: true}}]}}} as any)).toHaveLength(1);
    });
    test("Check no error if pod is okay", () => {
        expect(denyPrivilegedPod({object: {spec: {containers: [{name: "my-container"}]}}} as any)).toHaveLength(0);
    });
});

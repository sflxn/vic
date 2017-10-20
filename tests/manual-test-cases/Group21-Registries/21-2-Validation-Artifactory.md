Test 21-02 - Validation Against Artifactory's Docker Registry
=======

# Purpose:
To verify that VIC appliance can log into registries and pull private and public images

# References:
[1 - Docker Command Line Reference](https://docs.docker.com/engine/reference/commandline/login/)

# Environment:
This test requires that a vSphere server is running and available

# Test Steps:
1. Deploy VIC appliance to vSphere server
2. Issue docker pull private image on internal VMware artifactory server
3. Issue docker login on internal VMware artifactory server with invalid credentials 
4. Issue docker login on internal VMware artifactory server with valid credentials
5. Issue docker pull private image on internal VMware artifactory server
6. Issue docker logout on internal VMware artifactory server
7. Issue docker pull public image on internal VMware artifactory server

# Expected Outcome:
* Step 2 should result in an error without login
* Step 3 should result in an error of invalid credentials
* Step 4-7 should each succeed

# Possible Problems:
Test will fail if docker account victest is disabled, or if connection to docker.io cannot be 
established.

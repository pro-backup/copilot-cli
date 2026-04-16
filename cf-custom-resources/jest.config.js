// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0
module.exports = {
  roots: ["<rootDir>/lib", "<rootDir>/test"],
  transform: {
    "^.+\\.tsx?$": "ts-jest",
  },
  testRegex: "(/test/.*|(\\.|/)(test|spec))\\.(ts|js)x?$",
  moduleFileExtensions: ["ts", "tsx", "js", "jsx", "json", "node"],
  testEnvironment: "node",
  collectCoverageFrom: [
    "lib/**/*.js",
    "!lib/**/node_modules/**",
  ],
  coverageThreshold: {
    global: {
      statements: 90,
      branches: 78,
      functions: 80,
      lines: 90,
    },
  },
};

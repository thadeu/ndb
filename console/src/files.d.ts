declare module '*.css' {
  const path: string;

  export default path;
}

declare module '*.svg' {
  const path: string;

  export default path;
}

declare module '../dist/*.js' {
  const path: string;

  export default path;
}

import logo from "./assets/brand/logo.svg?no-inline";

/** Drop mark and wordmark. */
export default function Brand() {
  return (
    <a className="product-brand" href="#/" aria-label="PocketContext Drop home">
      <img src={logo} width="32" height="32" alt="" />
      <span>PocketContext <span>Drop</span></span>
      <span className="beta-ribbon">Beta</span>
    </a>
  );
}

<!-- XSLT 3.0: two typed xsl:functions called per record, format-date, tokenize, string-join -->
<xsl:stylesheet version="3.0" xmlns:xsl="http://www.w3.org/1999/XSL/Transform" xmlns:xs="http://www.w3.org/2001/XMLSchema" xmlns:map="http://www.w3.org/2005/xpath-functions/map" xmlns:array="http://www.w3.org/2005/xpath-functions/array" xmlns:f="urn:f" exclude-result-prefixes="xs map array f">
  <xsl:output method="text"/>
  <xsl:function name="f:label" as="xs:string">
    <xsl:param name="o" as="element(order)"/>
    <xsl:sequence select="string-join((string($o/@id), format-date(xs:date($o/date), '[D01] [MNn,*-3] [Y0001]'), f:skus($o)), ' | ')"/>
  </xsl:function>
  <xsl:function name="f:skus" as="xs:string">
    <xsl:param name="o" as="element(order)"/>
    <xsl:sequence select="string-join(for $t in tokenize(string-join($o/items/item/@sku, ','), ',') return upper-case($t), '+')"/>
  </xsl:function>
  <xsl:template match="/">
    <xsl:for-each select="/orders/order"><xsl:value-of select="f:label(.)"/><xsl:text>&#10;</xsl:text></xsl:for-each>
  </xsl:template>
</xsl:stylesheet>

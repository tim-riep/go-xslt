<!-- string functions: concat, substring, translate, upper-case, string-join, normalize-space over all records -->
<xsl:stylesheet version="3.0" xmlns:xsl="http://www.w3.org/1999/XSL/Transform" xmlns:xs="http://www.w3.org/2001/XMLSchema" xmlns:map="http://www.w3.org/2005/xpath-functions/map" xmlns:array="http://www.w3.org/2005/xpath-functions/array" xmlns:f="urn:f" exclude-result-prefixes="xs map array f">
  <xsl:output method="text"/>
  <xsl:template match="/">
    <xsl:for-each select="/orders/order">
      <xsl:value-of select="concat(upper-case(substring(customer, 1, 4)), translate(substring-after(customer, ' '), '0123456789', 'abcdefghij'), ' ', string-join(items/item/@sku, ','), ' ', string-length(normalize-space(concat(' ', date, '  ', price, ' '))), '&#10;')"/>
    </xsl:for-each>
  </xsl:template>
</xsl:stylesheet>
